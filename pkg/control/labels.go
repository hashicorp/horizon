package control

import (
	fmt "fmt"
	"sort"
	"strings"

	"github.com/hashicorp/horizon/pkg/pb"
)

func FlattenLabels(labels *pb.LabelSet) string {
	sort.Sort(labels)

	var out []string

	for _, lbl := range labels.Labels {
		out = append(out, fmt.Sprintf("%s=%s", lbl.Name, lbl.Value))
	}

	return strings.Join(out, ",")
}

func FlattenLabelSets(sets []*pb.LabelSet) []string {
	var out []string

	for _, set := range sets {
		out = append(out, FlattenLabels(set))
	}

	return out
}

func ExplodeLabelSetss(in []string) []*pb.LabelSet {
	var ret []*pb.LabelSet

	for _, list := range in {
		ret = append(ret, ExplodeLabels(list))
	}

	return ret
}

func ExplodeLabels(list string) *pb.LabelSet {
	var set pb.LabelSet
	for _, pair := range strings.Split(list, ",") {
		var name, val string

		eqIdx := strings.IndexByte(pair, '=')
		if eqIdx != -1 {
			name = pair[:eqIdx]
			val = pair[eqIdx+1:]
		} else {
			name = pair
		}

		set.Labels = append(set.Labels, &pb.Label{
			Name:  name,
			Value: val,
		})
	}

	return &set
}
