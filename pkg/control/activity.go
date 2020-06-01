package control

import (
	context "context"
	"encoding/json"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/jinzhu/gorm"
	"github.com/lib/pq"
)

// A quick note. This activity system is different than the one used between the hubs and control.
// This system is for managing an activity log that is in postgresql and shared between central
// teir instances. We use it instead of a message queue system right now for simplicity.

type ActivityLog struct {
	Id        int64 `gorm:"primary_key"`
	Event     []byte
	CreatedAt time.Time
}

type ActivityReader struct {
	db       *gorm.DB
	listener *pq.Listener

	lastEntry int64
	cancel    func()

	C chan []*ActivityLog

	wg sync.WaitGroup
}

var pgActivityChannel = "activaty_added"

func NewActivityReader(ctx context.Context, dbtype, conn string) (*ActivityReader, error) {
	db, err := gorm.Open(dbtype, conn)
	if err != nil {
		return nil, err
	}

	L := hclog.FromContext(ctx)

	reportProblem := func(ev pq.ListenerEventType, err error) {
		if err != nil {
			L.Error("problem observed while listen on postgres channel", "error", err)
		}
	}

	minReconn := 10 * time.Second
	maxReconn := time.Minute
	listener := pq.NewListener(conn, minReconn, maxReconn, reportProblem)

	err = listener.Listen(pgActivityChannel)
	if err != nil {
		db.Close()
		return nil, err
	}

	var entry ActivityLog

	err = dbx.Check(db.Last(&entry))
	if err != nil {
		if err != gorm.ErrRecordNotFound {
			return nil, err
		}
	}

	ctx, cancel := context.WithCancel(ctx)

	ar := &ActivityReader{
		db:        db,
		listener:  listener,
		C:         make(chan []*ActivityLog),
		lastEntry: entry.Id,
		cancel:    cancel,
	}

	ar.wg.Add(1)
	go ar.watch(ctx, L)

	return ar, nil
}

func (ar *ActivityReader) watch(ctx context.Context, L hclog.Logger) {
	defer ar.wg.Done()

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ar.listener.Notify:
			// got event
		case <-ticker.C:
			// timed out, check
		}

		ar.checkLog(ctx, L)
	}
}

func (ar *ActivityReader) checkLog(ctx context.Context, L hclog.Logger) {
	for {
		var entries []*ActivityLog

		err := dbx.Check(ar.db.Where("id > ?", ar.lastEntry).Limit(100).Find(&entries))
		if err != nil {
			if err != gorm.ErrRecordNotFound {
				L.Error("error looking for new activity log entries", "error", err)
			}

			return
		}

		if len(entries) == 0 {
			return
		}

		select {
		case <-ctx.Done():
			return
		case ar.C <- entries:
			// ok
		}

		ar.lastEntry = entries[len(entries)-1].Id
	}
}

func (ar *ActivityReader) Close() error {
	ar.cancel()
	ar.wg.Wait()
	return ar.db.Close()
}

type ActivityInjector struct {
	db *gorm.DB
}

func NewActivityInjector(db *gorm.DB) (*ActivityInjector, error) {
	ai := &ActivityInjector{
		db: db,
	}

	return ai, nil
}

func (ai *ActivityInjector) Inject(ctx context.Context, v interface{}) error {
	var entry ActivityLog

	switch sv := v.(type) {
	case []byte:
		entry.Event = sv
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		entry.Event = data
	}

	tx := ai.db.Begin()
	tx.Create(&entry)
	tx.Exec("NOTIFY " + pgActivityChannel)

	return dbx.Check(tx.Commit())
}
