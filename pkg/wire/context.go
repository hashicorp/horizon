package wire

type Context interface {
	AccountId() string
	ReadRequest(v Unmarshaller) (byte, error)
	WriteResponse(tag byte, v Marshaller) error
}

type ctx struct {
	accountId string
	rm        ReadMarshaler
	wm        WriteMarshaler
}

func NewContext(accountId string, rm ReadMarshaler, wm WriteMarshaler) Context {
	return &ctx{
		accountId: accountId,
		rm:        rm,
		wm:        wm,
	}
}

func (c *ctx) AccountId() string {
	return c.accountId
}

func (c *ctx) ReadRequest(v Unmarshaller) (byte, error) {
	tag, _, err := c.rm.ReadMarshal(v)
	if err != nil {
		return 0, err
	}

	return tag, nil
}

func (c *ctx) WriteResponse(tag byte, v Marshaller) error {
	_, err := c.wm.WriteMarshal(tag, v)
	return err
}
