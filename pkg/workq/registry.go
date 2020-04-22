package workq

import (
	"encoding/json"
	"reflect"
	"sync"

	"github.com/pkg/errors"
)

type Handler interface {
	PerformJob(jobType string, data []byte) error
}

type registeredHandler struct {
	argType reflect.Type
	f       reflect.Value
}

type Registry struct {
	mu    sync.RWMutex
	types map[string]registeredHandler
}

func (r *Registry) Register(jobType string, h interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.types == nil {
		r.types = make(map[string]registeredHandler)
	}

	v := reflect.ValueOf(h)
	if v.Kind() != reflect.Func {
		panic("register must be passed a func")
	}

	ft := v.Type()

	if ft.NumIn() != 2 {
		panic("register func takes 2 arguments")
	}

	if ft.In(0) != reflect.TypeOf("") {
		panic("register func first arg must be a string")
	}

	var err error

	if ft.Out(0) != reflect.TypeOf(&err).Elem() {
		panic("register out must return an error")
	}

	argt := ft.In(1)

	r.types[jobType] = registeredHandler{
		argType: argt,
		f:       v,
	}
}

func (r *Registry) Handle(job *Job) error {
	r.mu.RLock()

	rh, ok := r.types[job.JobType]

	r.mu.RUnlock()

	if !ok {
		return nil
	}

	arg := reflect.New(rh.argType.Elem())

	err := json.Unmarshal(job.Payload, arg.Interface())
	if err != nil {
		return errors.Wrapf(err, "wrong json for job type: %s", job.JobType)
	}

	out := rh.f.Call([]reflect.Value{
		reflect.ValueOf(job.JobType), arg,
	})

	v := out[0]

	if v.IsNil() {
		return nil
	}

	return v.Interface().(error)
}

func (r *Registry) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.types)
}
