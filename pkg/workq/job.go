package workq

import (
	"encoding/json"
	"time"

	"github.com/hashicorp/horizon/pkg/pb"
)

type Job struct {
	Id      []byte `gorm:"primary_key"`
	Queue   string
	Status  string
	JobType string
	Payload []byte

	CoolOffUntil *time.Time
	Attempts     int

	CreatedAt time.Time
}

func (j *Job) Set(jt string, v interface{}) error {
	j.JobType = jt

	data, err := json.Marshal(v)
	if err != nil {
		return err
	}

	j.Payload = data
	return nil
}

func NewJob() *Job {
	var j Job
	j.Id = pb.NewULID().Bytes()
	j.Status = "queued"

	return &j
}
