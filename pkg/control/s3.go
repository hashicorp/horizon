package control

import (
	bytes "bytes"
	context "context"
	"crypto/md5"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	fmt "fmt"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/jinzhu/gorm"
	"github.com/lib/pq"
	"github.com/pkg/errors"
)

func (s *Server) calculateAccountRouting(ctx context.Context, gdb *sql.DB, account *pb.Account, action string) ([]byte, error) {
	s.L.Debug("calculate account routing", "action", action, "account", account.SpecString())

	ts := time.Now()

	defer func() {
		s.L.Debug("calculate account routing ended", "action", action, "account", account.SpecString(), "elapse", time.Since(ts))
	}()

	key := account.Key()

	var lastId int64

	var accountServices pb.AccountServices

	// See https://www.citusdata.com/blog/2016/03/30/five-ways-to-paginate/ for decisions
	// on why we use this particular method to get all the records for an account. It's
	// important to note that there is an index on (account,id) on the services table
	// that allows this query to scan the index it's natural order.
	for {
		// Gotta poll the context since database/sql and gorm don't expose a context
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		rows, err := gdb.QueryContext(ctx, "SELECT id, hub_id, service_id, labels, type FROM services WHERE account_id = $1 AND id > $2 LIMIT 1000", key, lastId)
		if err != nil {
			return nil, err
		}

		var (
			hubId     []byte
			serviceId []byte
			labels    pq.StringArray
			typ       string
			cnt       int
		)

		for rows.Next() {
			cnt++
			err = rows.Scan(&lastId, &hubId, &serviceId, &labels, &typ)
			if err != nil {
				return nil, err
			}

			var ls pb.LabelSet

			err = ls.Scan(labels)
			if err != nil {
				return nil, err
			}

			accountServices.Services = append(accountServices.Services, &pb.ServiceRoute{
				Hub:    pb.ULIDFromBytes(hubId),
				Id:     pb.ULIDFromBytes(serviceId),
				Type:   typ,
				Labels: &ls,
			})
		}

		if cnt == 0 {
			break
		}
	}

	data, err := accountServices.Marshal()
	if err != nil {
		return nil, err
	}

	return zstdCompress(data)
}

func (s *Server) updateAccountRouting(ctx context.Context, db *sql.DB, account *pb.Account, action string) error {
	ts := time.Now()
	s.L.Debug("updating account routing", "action", action, "account", account.SpecString())

	defer func() {
		s.m.MeasureSince([]string{"routing", "update_time"}, ts)
		s.L.Debug("updating account routing ended", "action", action, "account", account.SpecString(), "elapse", time.Since(ts))
	}()

	outData, err := s.calculateAccountRouting(ctx, db, account, "initial-update")
	if err != nil {
		return err
	}

	h := md5.New()
	h.Write(outData)
	sum := h.Sum(nil)

	accountKey := account.HashKey()

	key := fmt.Sprintf("account_services/%s", accountKey)

	lockKey := "account-" + accountKey

	strMD5 := base64.StdEncoding.EncodeToString(sum)

	var retry int

	for {
		lock, err := s.lockMgr.GetLock(lockKey, strMD5)
		if err == nil {
			defer lock.Close()
			break
		}

		info, err := s.lockMgr.GetValue(lockKey)
		if err != nil {
			return err
		}

		if info == strMD5 {
			// Ok, someone else got all the records, PEACE OUT.
			return nil
		}

		s.L.Info("detected account locked, sleep and retry", "retries", retry)
		retry++

		time.Sleep(time.Second)

		outData, err := s.calculateAccountRouting(ctx, db, account, "retry")
		if err != nil {
			return err
		}

		h := md5.New()
		h.Write(outData)
		sum := h.Sum(nil)

		strMD5 = base64.StdEncoding.EncodeToString(sum)
		continue
	}

	s3obj := s3.New(s.awsSess)

	inputEtag := base64.StdEncoding.EncodeToString(sum)

	putIn := &s3.PutObjectInput{
		ACL:         aws.String("private"),
		Body:        bytes.NewReader(outData),
		ContentMD5:  aws.String(inputEtag),
		ContentType: aws.String("application/horizon"),
		Bucket:      &s.bucket,
		Key:         &key,
		Tagging:     aws.String("usage=horizon"),
	}

	if s.kmsKeyId != "" {
		putIn.SSEKMSKeyId = aws.String(s.kmsKeyId)
		putIn.ServerSideEncryption = aws.String("aws:kms")
	}

	putOut, err := s3obj.PutObject(putIn)
	if err != nil {
		return errors.Wrapf(err, "unable to upload object")
	}

	outet := *putOut.ETag

	outSum, err := hex.DecodeString(outet[1 : len(outet)-1])
	if err != nil {
		return err
	}

	if !bytes.Equal(sum, outSum) {
		return fmt.Errorf("corruption detected, wrong etag: %s / %s", hex.EncodeToString(sum), outet)
	}

	return nil
}

func (s *Server) updateLabelLinks(ctx context.Context) error {
	lastId := 0

	lls := make([]*LabelLink, 0, 100)

	var out pb.LabelLinks

	for {
		err := dbx.Check(s.db.Where("id > ?", lastId).Limit(100).Find(&lls))
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				s.L.Info("end of label link loop cursor reached", "last-id", lastId)
				break
			}
			s.L.Error("error returned from label_link cursor", "error", err)
		}

		if len(lls) == 0 {
			s.L.Info("label link cursor returned no new values, done", "last-id", lastId)
			break
		}

		for _, ll := range lls {
			account, err := pb.AccountFromKey(ll.AccountID)
			if err != nil {
				s.L.Error("error parsing label-link account", "error", err)
				return err
			}

			var acc Account

			err = dbx.Check(s.db.First(&acc, ll.AccountID))
			if err != nil {
				s.L.Error("error reading label-link account", "error", err, "acconut", string(ll.AccountID))
				return err
			}

			var pblimit pb.Account_Limits
			acc.Data.Get("limits", &pblimit)

			out.LabelLinks = append(out.LabelLinks, &pb.LabelLink{
				Account: account,
				Labels:  ExplodeLabels(ll.Labels),
				Target:  ExplodeLabels(ll.Target),
				Limits:  &pblimit,
			})
		}

		lastId = lls[len(lls)-1].ID

		lls = lls[:0]
	}

	data, err := out.Marshal()
	if err != nil {
		return err
	}

	outData, err := zstdCompress(data)
	if err != nil {
		return err
	}

	h := md5.New()
	h.Write(outData)
	sum := h.Sum(nil)

	s3obj := s3.New(s.awsSess)

	inputEtag := base64.StdEncoding.EncodeToString(sum)

	putIn := &s3.PutObjectInput{
		ACL:         aws.String("private"),
		Body:        bytes.NewReader(outData),
		ContentMD5:  aws.String(inputEtag),
		ContentType: aws.String("application/horizon"),
		Bucket:      &s.bucket,
		Key:         aws.String("label_links"),
		Tagging:     aws.String("usage=horizon"),
	}

	if s.kmsKeyId != "" {
		putIn.SSEKMSKeyId = aws.String(s.kmsKeyId)
		putIn.ServerSideEncryption = aws.String("aws:kms")
	}

	putOut, err := s3obj.PutObject(putIn)
	if err != nil {
		return errors.Wrapf(err, "unable to upload object")
	}

	outet := *putOut.ETag

	outSum, err := hex.DecodeString(outet[1 : len(outet)-1])
	if err != nil {
		return err
	}

	if !bytes.Equal(sum, outSum) {
		return fmt.Errorf("corruption detected, wrong etag: %s / %s", hex.EncodeToString(sum), outet)
	}

	s.L.Info("updated label links", "etag", outet, "size", len(outData), "last-id", lastId)

	return nil
}
