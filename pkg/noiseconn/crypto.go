package noiseconn

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"io"

	"github.com/flynn/noise"
	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/horizon/pkg/wire"
	"github.com/pkg/errors"
	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/curve25519"
)

var cipherSuite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2b)

var ErrProtocolError = errors.New("protocol error")

func GenerateKey() (noise.DHKey, error) {
	return cipherSuite.GenerateKeypair(rand.Reader)
}

func PublicKey(key noise.DHKey) string {
	return base64.RawURLEncoding.EncodeToString(key.Public)
}

func PrivateKey(key noise.DHKey) string {
	return base64.RawURLEncoding.EncodeToString(key.Private)
}

func ParsePrivateKey(key string) (noise.DHKey, error) {
	data, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil {
		return noise.DHKey{}, err
	}

	var pubkey, privkey [32]byte

	copy(privkey[:], data)

	curve25519.ScalarBaseMult(&pubkey, &privkey)
	return noise.DHKey{Private: privkey[:], Public: pubkey[:]}, nil
}

const MaxMsgLen = noise.MaxMsgLen - 64

type Conn struct {
	L   hclog.Logger
	rwc io.ReadWriteCloser

	fr *wire.FramingReader
	fw *wire.FramingWriter

	hs      *noise.HandshakeState
	writeCS *noise.CipherState
	readCS  *noise.CipherState

	rszbuf [2]byte
	wszbuf [2]byte

	writeBuf []byte

	rest []byte
}

func NewConn(rwc io.ReadWriteCloser) (*Conn, error) {
	fr, err := wire.NewFramingReader(rwc)
	if err != nil {
		return nil, err
	}

	fw, err := wire.NewFramingWriter(rwc)
	if err != nil {
		return nil, err
	}

	conn := &Conn{
		L:        hclog.L().Named("noise"),
		rwc:      rwc,
		fr:       fr,
		fw:       fw,
		writeBuf: make([]byte, noise.MaxMsgLen),
	}

	return conn, nil
}

const (
	setupTag = 1
	dataTag  = 2
)

func (t *Conn) Accept(key noise.DHKey) error {
	var cfg noise.Config

	cfg.CipherSuite = cipherSuite

	cfg.Pattern = noise.HandshakeXK
	cfg.StaticKeypair = key

	hs, err := noise.NewHandshakeState(cfg)
	if err != nil {
		return err
	}

	t.hs = hs

	t.L.Trace("beginnning noise accept handshake")

	tag, sz, err := t.fr.Next()
	if err != nil {
		return err
	}

	if tag != setupTag {
		return ErrProtocolError
	}

	buf := make([]byte, sz)

	_, err = io.ReadFull(t.fr, buf)
	if err != nil {
		return err
	}

	_, _, _, err = t.hs.ReadMessage(nil, buf)
	if err != nil {
		return err
	}

	data, _, _, err := t.hs.WriteMessage(nil, nil)
	if err != nil {
		return err
	}

	t.L.Trace("mid accept noise handshake")

	err = t.fw.WriteFrame(setupTag, len(data))
	if err != nil {
		return err
	}

	_, err = t.fw.Write(data)
	if err != nil {
		return err
	}

	tag, sz, err = t.fr.Next()
	if err != nil {
		return err
	}

	if tag != setupTag {
		return ErrProtocolError
	}

	data = make([]byte, sz)

	_, err = io.ReadFull(t.fr, data)
	if err != nil {
		return err
	}

	_, t.writeCS, t.readCS, err = t.hs.ReadMessage(nil, data)
	if err != nil {
		return err
	}

	t.L.Trace("finished accept handshake")
	return nil
}

func hash(d []byte) string {
	h, _ := blake2b.New256(nil)
	h.Write(d)
	return hex.EncodeToString(h.Sum(nil))
}

func (t *Conn) Connect(key noise.DHKey, peer string) error {
	var cfg noise.Config

	cfg.CipherSuite = cipherSuite

	cfg.Pattern = noise.HandshakeXK
	cfg.StaticKeypair = key
	pkey, err := base64.RawURLEncoding.DecodeString(peer)
	if err != nil {
		return err
	}

	cfg.PeerStatic = pkey
	cfg.Initiator = true

	hs, err := noise.NewHandshakeState(cfg)
	if err != nil {
		return err
	}

	t.hs = hs

	data, _, _, err := t.hs.WriteMessage(nil, nil)

	t.L.Trace("begin noise connect handshake")

	err = t.fw.WriteFrame(setupTag, len(data))
	if err != nil {
		return err
	}

	_, err = t.fw.Write(data)
	if err != nil {
		return err
	}

	tag, sz, err := t.fr.Next()
	if err != nil {
		return err
	}

	if tag != setupTag {
		return ErrProtocolError
	}

	data = make([]byte, sz)

	_, err = io.ReadFull(t.fr, data)
	if err != nil {
		return err
	}

	t.L.Trace("mid connect handshake")

	_, _, _, err = t.hs.ReadMessage(nil, data)
	if err != nil {
		return err
	}

	data, t.readCS, t.writeCS, err = t.hs.WriteMessage(nil, nil)
	if err != nil {
		return err
	}

	err = t.fw.WriteFrame(setupTag, len(data))
	if err != nil {
		return err
	}

	_, err = t.fw.Write(data)
	if err != nil {
		return err
	}

	t.L.Trace("finished connect handshake")
	return nil
}

func (t *Conn) Encrypt(data []byte) []byte {
	if t.writeCS == nil {
		return data
	}

	return t.writeCS.Encrypt(nil, nil, data)
}

func (t *Conn) Decrypt(data []byte) ([]byte, error) {
	if t.readCS == nil {
		return data, nil
	}

	return t.readCS.Decrypt(nil, nil, data)
}

func (t *Conn) Read(buf []byte) (int, error) {
	if len(t.rest) > 0 {
		if len(t.rest) <= len(buf) {
			n := copy(buf, t.rest)
			t.rest = nil

			return n, nil
		}

		n := copy(buf, t.rest)
		t.rest = t.rest[len(buf):]

		return n, nil
	}

	tag, sz, err := t.fr.Next()
	if err != nil {
		return 0, err
	}

	if tag != dataTag {
		return 0, ErrProtocolError
	}

	var (
		tbuf    []byte
		copyOut bool
	)

	if len(buf) >= sz {
		tbuf = buf[:sz]
	} else {
		copyOut = true
		tbuf = make([]byte, sz)
	}

	_, err = io.ReadFull(t.fr, tbuf)
	if err != nil {
		t.L.Error("error reading data", "error", err)
		return sz + 2, err
	}

	data, err := t.readCS.Decrypt(tbuf[:0], nil, tbuf)
	if err != nil {
		t.L.Error("error decrypting data", "error", err)
		return sz + 2, err
	}

	if copyOut {
		copy(buf, data)
		t.rest = data[len(buf):]
	}

	return len(data), nil
}

func (t *Conn) Write(buf []byte) (int, error) {
	var total int

	for len(buf) > 0 {
		var sbuf []byte

		if len(buf) > MaxMsgLen {
			sbuf = buf[:MaxMsgLen]
			buf = buf[MaxMsgLen:]
		} else {
			sbuf = buf
			buf = nil
		}

		enc := t.writeCS.Encrypt(t.writeBuf[:0], nil, sbuf)

		err := t.fw.WriteFrame(dataTag, len(enc))
		if err != nil {
			return total, err
		}

		_, err = t.fw.Write(enc)
		if err != nil {
			return total, err
		}

		total += len(sbuf)
	}

	return total, nil
}
