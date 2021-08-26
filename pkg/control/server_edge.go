package control

import (
	context "context"

	"github.com/hashicorp/horizon/pkg/dbx"
	"github.com/hashicorp/horizon/pkg/pb"
)

func (s *Server) LookupEndpoints(ctx context.Context, req *pb.LookupEndpointsRequest) (*pb.LookupEndpointsResponse, error) {
	_, err := s.checkFromHub(ctx, "lookup-endpoints")
	if err != nil {
		return nil, err
	}

	return s.queryDBServices(ctx, req)
}

func (s *Server) queryDBServices(ctx context.Context, req *pb.LookupEndpointsRequest) (*pb.LookupEndpointsResponse, error) {
	var services []*Service
	err := dbx.Check(
		s.db.Where("account_id = ? AND labels @> ?", req.Account.Key(), req.Labels.AsStringArray()).Find(&services))
	if err != nil {
		return nil, err
	}

	var resp pb.LookupEndpointsResponse

	for _, serv := range services {
		var ls pb.LabelSet

		err = ls.Scan(serv.Labels)
		if err != nil {
			return nil, err
		}

		resp.Routes = append(resp.Routes, &pb.ServiceRoute{
			Hub:    pb.ULIDFromBytes(serv.HubId),
			Id:     pb.ULIDFromBytes(serv.ServiceId),
			Type:   serv.Type,
			Labels: &ls,
		})
	}

	return &resp, nil
}

func (s *Server) ResolveLabelLink(ctx context.Context, req *pb.ResolveLabelLinkRequest) (*pb.ResolveLabelLinkResponse, error) {
	_, err := s.checkFromHub(ctx, "resolve-label-link")
	if err != nil {
		return nil, err
	}

	return s.queryDBLabelLinks(ctx, req)
}

func (s *Server) queryDBLabelLinks(ctx context.Context, req *pb.ResolveLabelLinkRequest) (*pb.ResolveLabelLinkResponse, error) {
	var ll LabelLink

	err := dbx.Check(
		s.db.Where("labels = ?", FlattenLabels(req.Labels)).Preload("Account").First(&ll))
	if err != nil {
		return nil, err
	}

	var resp pb.ResolveLabelLinkResponse

	target := ExplodeLabels(ll.Target)

	account, err := pb.AccountFromKey(ll.AccountID)
	if err != nil {
		return nil, err
	}

	var pblimit pb.Account_Limits
	ll.Account.Data.Get("limits", &pblimit)

	resp.Labels = target
	resp.Account = account
	resp.Limits = &pblimit

	return &resp, nil
}

var _ pb.EdgeServicesServer = (*Server)(nil)
