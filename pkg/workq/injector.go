package workq

import (
	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/hashicorp/horizon/pkg/pb"
	"github.com/jinzhu/gorm"
)

type Injector struct {
	db *gorm.DB
}

func (i *Injector) Inject(job *Job) error {
	if job.Id == nil {
		job.Id = pb.NewULID().Bytes()
	}

	tx := i.db.Begin()

	tx.Create(&job)

	tx.Exec("NOTIFY " + listenChannel)

	return dbx.Check(tx.Commit())
}
