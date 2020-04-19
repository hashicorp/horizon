package workq

import (
	"time"

	"github.com/hashicorp/horizon/pkg/pb"
)

type Job struct {
	Id      []byte `gorm:"primary_key"`
	Queue   string
	Payload string
	Status  string

	CreatedAt time.Time
}

func NewJob() *Job {
	var j Job
	j.Id = pb.NewULID().Bytes()
	j.Status = "queued"

	return &j
}
