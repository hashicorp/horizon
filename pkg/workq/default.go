package workq

import (
	"encoding/json"
	"sync"
	"time"
)

// A default registry that other packages can easily register their types
// against.
var GlobalRegistry = &Registry{}

// Register a job and handler with the default registry.
func RegisterHandler(jobType string, h interface{}) {
	GlobalRegistry.Register(jobType, h)
}

type defaultPeriodic struct {
	name, queue, jobType string
	payload              []byte
	period               time.Duration
}

var periodMu sync.Mutex

var defaultPeriodics []defaultPeriodic

func RegisterPeriodicJob(name, queue, jobType string, v interface{}, period time.Duration) {
	periodMu.Lock()
	defer periodMu.Unlock()

	payload, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}

	defaultPeriodics = append(defaultPeriodics, defaultPeriodic{
		name, queue, jobType, payload, period,
	})
}
