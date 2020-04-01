package postgres

import (
	"time"

	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/jinzhu/gorm"
	"github.com/lib/pq"
	"github.com/oklog/ulid"

	_ "github.com/jinzhu/gorm/dialects/postgres"
)

type Router struct {
	db *gorm.DB
}

type Agent struct {
	ID        uint `gorm:"primary_key"`
	CreatedAt time.Time
	UpdatedAt time.Time

	SessionId wire.ULID
}

type Service struct {
	CreatedAt time.Time
	UpdatedAt time.Time

	Agent   *Agent
	AgentID uint

	ServiceId   wire.ULID
	Type        string
	Description string
	Labels      pq.StringArray
}

func NewRouter(dbtype, connDetails string) (*Router, error) {
	db, err := gorm.Open(dbtype, connDetails)
	if err != nil {
		return nil, err
	}

	return &Router{db: db}, nil
}

func (r *Router) RegisterService(agent ulid.ULID, serv *wire.ServiceInfo) error {
	var ao Agent
	ao.SessionId.ULID = agent

	r.db.Set("gorm:insert_option", "ON CONFLICT DO NOTHING").Create(&ao)

	var so Service

	so.AgentID = ao.ID
	so.ServiceId = serv.ServiceId
	so.Type = serv.Type
	so.Description = serv.Description
	so.Labels = serv.Labels

	return r.db.Create(&so).Error
}

type ServiceLocation struct {
	Agent   ulid.ULID
	Service *wire.ServiceInfo
}

func (r *Router) LookupService(labels []string) ([]*ServiceLocation, error) {

	var dbserv []Service

	r.db.Where("labels = ?", pq.StringArray(labels)).Find(&dbserv)

	var services []*ServiceLocation

	for _, s := range dbserv {
		var dbagent Agent

		r.db.Model(s).Related(&dbagent)

		services = append(services, &ServiceLocation{
			Agent: dbagent.SessionId.ULID,
			Service: &wire.ServiceInfo{
				ServiceId:   s.ServiceId,
				Type:        s.Type,
				Description: s.Description,
				Labels:      s.Labels,
			},
		})
	}

	return services, nil
}

func (r *Router) KnownTarget(target string) bool {
	return false
}

func (r *Router) AddAccount(id, defTarget string) error {
	return nil
}

func (r *Router) CreateDefaultRoute(id, labels string) error {
	return nil
}

func (r *Router) CheckAccount(id string) bool {
	return false
}

func (r *Router) LabelsForTarget(target string) (string, string, error) {
	return "", "", nil
}
