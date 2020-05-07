package pb

func (l *NetworkLocation) SameLabels(r *NetworkLocation) bool {
	if l.Labels == nil || r.Labels == nil {
		return false
	}

	return l.Labels.Equal(r.Labels)
}

func (l *NetworkLocation) Cardinality(r *NetworkLocation) int {
	if l.Labels == nil || r.Labels == nil {
		return 0
	}

	var card int

	for _, llbl := range l.Labels.Labels {
		for _, rlbl := range r.Labels.Labels {
			if llbl.Equal(rlbl) {
				card++
			}
		}
	}

	return card
}

func (l *NetworkLocation) IsPublic() bool {
	for _, lbl := range l.Labels.Labels {
		if lbl.Name == "type" {
			return lbl.Value == "public"
		}
	}

	return false
}
