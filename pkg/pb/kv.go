package pb

import (
	"encoding/base64"
	"strconv"
)

func (kv *KVPair) ValueString() string {
	switch {
	case kv.Value != "":
		return kv.Value
	case kv.Bvalue != nil:
		return base64.StdEncoding.EncodeToString(kv.Bvalue)
	case kv.Ivalue != 0:
		return strconv.FormatInt(kv.Ivalue, 10)
	default:
		return ""
	}
}
