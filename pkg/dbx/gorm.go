package dbx

import "github.com/hashicorp/go-multierror"

type HasErrors interface {
	GetErrors() []error
}

func Check(x HasErrors) error {
	errs := x.GetErrors()
	switch len(errs) {
	case 0:
		return nil
	case 1:
		return errs[0]
	default:
		return multierror.Append(nil, errs...)
	}
}
