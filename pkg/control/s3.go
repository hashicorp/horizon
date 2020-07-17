package control

import (
	bytes "bytes"
	context "context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	fmt "fmt"
	"time"

	"cirello.io/dynamolock"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/jinzhu/gorm"
	"github.com/pkg/errors"
)

func (s *Server) calculateAccountRouting(ctx context.Context, db *gorm.DB, account *pb.Account) ([]byte, error) {
	key := account.Key()

	var lastId int64

	services := make([]*Service, 0, 100)

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

		err := dbx.Check(db.Where("account_id = ?", key).Where("id > ?", lastId).Limit(100).Find(&services))
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				break
			}
		}

		if len(services) == 0 {
			break
		}

		for _, serv := range services {
			var ls pb.LabelSet

			err = ls.Scan(serv.Labels)
			if err != nil {
				return nil, err
			}

			accountServices.Services = append(accountServices.Services, &pb.ServiceRoute{
				Hub:    pb.ULIDFromBytes(serv.HubId),
				Id:     pb.ULIDFromBytes(serv.ServiceId),
				Type:   serv.Type,
				Labels: &ls,
			})
		}

		lastId = services[len(services)-1].ID

		services = services[:0]
	}

	data, err := accountServices.Marshal()
	if err != nil {
		return nil, err
	}

	return zstdCompress(data)
}

func (s *Server) updateAccountRouting(ctx context.Context, db *gorm.DB, account *pb.Account) error {
	outData, err := s.calculateAccountRouting(ctx, db, account)
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

	for {
		lock, err := s.lockMgr.AcquireLock(lockKey,
			dynamolock.WithAdditionalAttributes(
				map[string]*dynamodb.AttributeValue{
					"md5": {S: &strMD5},
				}),
			dynamolock.FailIfLocked(),
		)

		if err == nil {
			defer lock.Close()
			break
		}

		info, err := s.lockMgr.Get(lockKey)
		if err != nil {
			return err
		}

		attrs := info.AdditionalAttributes()
		if val, ok := attrs["md5"]; ok {
			if val.S != nil && *val.S == strMD5 {
				// Ok, someone else got all the records, PEACE OUT.
				return nil
			}
		}

		time.Sleep(5 * time.Second)

		outData, err := s.calculateAccountRouting(ctx, db, account)
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
		// Gotta poll the context since database/sql and gorm don't expose a context
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := dbx.Check(s.db.Where("id > ?", lastId).Limit(100).Find(&lls))
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				break
			}
		}

		if len(lls) == 0 {
			break
		}

		for _, ll := range lls {
			account, err := pb.AccountFromKey(ll.AccountID)
			if err != nil {
				return err
			}

			var acc Account

			err = dbx.Check(s.db.First(&acc, ll.AccountID))
			if err != nil {
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

	return nil
}
