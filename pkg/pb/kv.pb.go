// Code generated by protoc-gen-gogo. DO NOT EDIT.
// source: kv.proto

package pb

import (
	bytes "bytes"
	fmt "fmt"
	proto "github.com/gogo/protobuf/proto"
	io "io"
	math "math"
	math_bits "math/bits"
	reflect "reflect"
	strings "strings"
)

// Reference imports to suppress errors if they are not otherwise used.
var _ = proto.Marshal
var _ = fmt.Errorf
var _ = math.Inf

// This is a compile-time assertion to ensure that this generated file
// is compatible with the proto package it is being compiled against.
// A compilation error at this line likely means your copy of the
// proto package needs to be updated.
const _ = proto.GoGoProtoPackageIsVersion3 // please upgrade the proto package

type KVPair struct {
	Key    string `protobuf:"bytes,1,opt,name=key,proto3" json:"key,omitempty"`
	Ikey   int64  `protobuf:"varint,2,opt,name=ikey,proto3" json:"ikey,omitempty"`
	Value  string `protobuf:"bytes,3,opt,name=value,proto3" json:"value,omitempty"`
	Ivalue int64  `protobuf:"varint,4,opt,name=ivalue,proto3" json:"ivalue,omitempty"`
	Bvalue []byte `protobuf:"bytes,5,opt,name=bvalue,proto3" json:"bvalue,omitempty"`
}

func (m *KVPair) Reset()      { *m = KVPair{} }
func (*KVPair) ProtoMessage() {}
func (*KVPair) Descriptor() ([]byte, []int) {
	return fileDescriptor_2216fe83c9c12408, []int{0}
}
func (m *KVPair) XXX_Unmarshal(b []byte) error {
	return m.Unmarshal(b)
}
func (m *KVPair) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	if deterministic {
		return xxx_messageInfo_KVPair.Marshal(b, m, deterministic)
	} else {
		b = b[:cap(b)]
		n, err := m.MarshalToSizedBuffer(b)
		if err != nil {
			return nil, err
		}
		return b[:n], nil
	}
}
func (m *KVPair) XXX_Merge(src proto.Message) {
	xxx_messageInfo_KVPair.Merge(m, src)
}
func (m *KVPair) XXX_Size() int {
	return m.Size()
}
func (m *KVPair) XXX_DiscardUnknown() {
	xxx_messageInfo_KVPair.DiscardUnknown(m)
}

var xxx_messageInfo_KVPair proto.InternalMessageInfo

func (m *KVPair) GetKey() string {
	if m != nil {
		return m.Key
	}
	return ""
}

func (m *KVPair) GetIkey() int64 {
	if m != nil {
		return m.Ikey
	}
	return 0
}

func (m *KVPair) GetValue() string {
	if m != nil {
		return m.Value
	}
	return ""
}

func (m *KVPair) GetIvalue() int64 {
	if m != nil {
		return m.Ivalue
	}
	return 0
}

func (m *KVPair) GetBvalue() []byte {
	if m != nil {
		return m.Bvalue
	}
	return nil
}

func init() {
	proto.RegisterType((*KVPair)(nil), "pb.KVPair")
}

func init() { proto.RegisterFile("kv.proto", fileDescriptor_2216fe83c9c12408) }

var fileDescriptor_2216fe83c9c12408 = []byte{
	// 180 bytes of a gzipped FileDescriptorProto
	0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0xff, 0xe2, 0xe2, 0xc8, 0x2e, 0xd3, 0x2b,
	0x28, 0xca, 0x2f, 0xc9, 0x17, 0x62, 0x2a, 0x48, 0x52, 0x2a, 0xe1, 0x62, 0xf3, 0x0e, 0x0b, 0x48,
	0xcc, 0x2c, 0x12, 0x12, 0xe0, 0x62, 0xce, 0x4e, 0xad, 0x94, 0x60, 0x54, 0x60, 0xd4, 0xe0, 0x0c,
	0x02, 0x31, 0x85, 0x84, 0xb8, 0x58, 0x32, 0x41, 0x42, 0x4c, 0x0a, 0x8c, 0x1a, 0xcc, 0x41, 0x60,
	0xb6, 0x90, 0x08, 0x17, 0x6b, 0x59, 0x62, 0x4e, 0x69, 0xaa, 0x04, 0x33, 0x58, 0x1d, 0x84, 0x23,
	0x24, 0xc6, 0xc5, 0x96, 0x09, 0x11, 0x66, 0x01, 0xab, 0x85, 0xf2, 0x40, 0xe2, 0x49, 0x10, 0x71,
	0x56, 0x05, 0x46, 0x0d, 0x9e, 0x20, 0x28, 0xcf, 0xc9, 0xe4, 0xc2, 0x43, 0x39, 0x86, 0x1b, 0x0f,
	0xe5, 0x18, 0x3e, 0x3c, 0x94, 0x63, 0x6c, 0x78, 0x24, 0xc7, 0xb8, 0xe2, 0x91, 0x1c, 0xe3, 0x89,
	0x47, 0x72, 0x8c, 0x17, 0x1e, 0xc9, 0x31, 0x3e, 0x78, 0x24, 0xc7, 0xf8, 0xe2, 0x91, 0x1c, 0xc3,
	0x87, 0x47, 0x72, 0x8c, 0x13, 0x1e, 0xcb, 0x31, 0x5c, 0x78, 0x2c, 0xc7, 0x70, 0xe3, 0xb1, 0x1c,
	0x43, 0x12, 0x1b, 0xd8, 0xd9, 0xc6, 0x80, 0x00, 0x00, 0x00, 0xff, 0xff, 0x2f, 0x85, 0xb0, 0xd4,
	0xc2, 0x00, 0x00, 0x00,
}

func (this *KVPair) Equal(that interface{}) bool {
	if that == nil {
		return this == nil
	}

	that1, ok := that.(*KVPair)
	if !ok {
		that2, ok := that.(KVPair)
		if ok {
			that1 = &that2
		} else {
			return false
		}
	}
	if that1 == nil {
		return this == nil
	} else if this == nil {
		return false
	}
	if this.Key != that1.Key {
		return false
	}
	if this.Ikey != that1.Ikey {
		return false
	}
	if this.Value != that1.Value {
		return false
	}
	if this.Ivalue != that1.Ivalue {
		return false
	}
	if !bytes.Equal(this.Bvalue, that1.Bvalue) {
		return false
	}
	return true
}
func (this *KVPair) GoString() string {
	if this == nil {
		return "nil"
	}
	s := make([]string, 0, 9)
	s = append(s, "&pb.KVPair{")
	s = append(s, "Key: "+fmt.Sprintf("%#v", this.Key)+",\n")
	s = append(s, "Ikey: "+fmt.Sprintf("%#v", this.Ikey)+",\n")
	s = append(s, "Value: "+fmt.Sprintf("%#v", this.Value)+",\n")
	s = append(s, "Ivalue: "+fmt.Sprintf("%#v", this.Ivalue)+",\n")
	s = append(s, "Bvalue: "+fmt.Sprintf("%#v", this.Bvalue)+",\n")
	s = append(s, "}")
	return strings.Join(s, "")
}
func valueToGoStringKv(v interface{}, typ string) string {
	rv := reflect.ValueOf(v)
	if rv.IsNil() {
		return "nil"
	}
	pv := reflect.Indirect(rv).Interface()
	return fmt.Sprintf("func(v %v) *%v { return &v } ( %#v )", typ, typ, pv)
}
func (m *KVPair) Marshal() (dAtA []byte, err error) {
	size := m.Size()
	dAtA = make([]byte, size)
	n, err := m.MarshalToSizedBuffer(dAtA[:size])
	if err != nil {
		return nil, err
	}
	return dAtA[:n], nil
}

func (m *KVPair) MarshalTo(dAtA []byte) (int, error) {
	size := m.Size()
	return m.MarshalToSizedBuffer(dAtA[:size])
}

func (m *KVPair) MarshalToSizedBuffer(dAtA []byte) (int, error) {
	i := len(dAtA)
	_ = i
	var l int
	_ = l
	if len(m.Bvalue) > 0 {
		i -= len(m.Bvalue)
		copy(dAtA[i:], m.Bvalue)
		i = encodeVarintKv(dAtA, i, uint64(len(m.Bvalue)))
		i--
		dAtA[i] = 0x2a
	}
	if m.Ivalue != 0 {
		i = encodeVarintKv(dAtA, i, uint64(m.Ivalue))
		i--
		dAtA[i] = 0x20
	}
	if len(m.Value) > 0 {
		i -= len(m.Value)
		copy(dAtA[i:], m.Value)
		i = encodeVarintKv(dAtA, i, uint64(len(m.Value)))
		i--
		dAtA[i] = 0x1a
	}
	if m.Ikey != 0 {
		i = encodeVarintKv(dAtA, i, uint64(m.Ikey))
		i--
		dAtA[i] = 0x10
	}
	if len(m.Key) > 0 {
		i -= len(m.Key)
		copy(dAtA[i:], m.Key)
		i = encodeVarintKv(dAtA, i, uint64(len(m.Key)))
		i--
		dAtA[i] = 0xa
	}
	return len(dAtA) - i, nil
}

func encodeVarintKv(dAtA []byte, offset int, v uint64) int {
	offset -= sovKv(v)
	base := offset
	for v >= 1<<7 {
		dAtA[offset] = uint8(v&0x7f | 0x80)
		v >>= 7
		offset++
	}
	dAtA[offset] = uint8(v)
	return base
}
func (m *KVPair) Size() (n int) {
	if m == nil {
		return 0
	}
	var l int
	_ = l
	l = len(m.Key)
	if l > 0 {
		n += 1 + l + sovKv(uint64(l))
	}
	if m.Ikey != 0 {
		n += 1 + sovKv(uint64(m.Ikey))
	}
	l = len(m.Value)
	if l > 0 {
		n += 1 + l + sovKv(uint64(l))
	}
	if m.Ivalue != 0 {
		n += 1 + sovKv(uint64(m.Ivalue))
	}
	l = len(m.Bvalue)
	if l > 0 {
		n += 1 + l + sovKv(uint64(l))
	}
	return n
}

func sovKv(x uint64) (n int) {
	return (math_bits.Len64(x|1) + 6) / 7
}
func sozKv(x uint64) (n int) {
	return sovKv(uint64((x << 1) ^ uint64((int64(x) >> 63))))
}
func (this *KVPair) String() string {
	if this == nil {
		return "nil"
	}
	s := strings.Join([]string{`&KVPair{`,
		`Key:` + fmt.Sprintf("%v", this.Key) + `,`,
		`Ikey:` + fmt.Sprintf("%v", this.Ikey) + `,`,
		`Value:` + fmt.Sprintf("%v", this.Value) + `,`,
		`Ivalue:` + fmt.Sprintf("%v", this.Ivalue) + `,`,
		`Bvalue:` + fmt.Sprintf("%v", this.Bvalue) + `,`,
		`}`,
	}, "")
	return s
}
func valueToStringKv(v interface{}) string {
	rv := reflect.ValueOf(v)
	if rv.IsNil() {
		return "nil"
	}
	pv := reflect.Indirect(rv).Interface()
	return fmt.Sprintf("*%v", pv)
}
func (m *KVPair) Unmarshal(dAtA []byte) error {
	l := len(dAtA)
	iNdEx := 0
	for iNdEx < l {
		preIndex := iNdEx
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return ErrIntOverflowKv
			}
			if iNdEx >= l {
				return io.ErrUnexpectedEOF
			}
			b := dAtA[iNdEx]
			iNdEx++
			wire |= uint64(b&0x7F) << shift
			if b < 0x80 {
				break
			}
		}
		fieldNum := int32(wire >> 3)
		wireType := int(wire & 0x7)
		if wireType == 4 {
			return fmt.Errorf("proto: KVPair: wiretype end group for non-group")
		}
		if fieldNum <= 0 {
			return fmt.Errorf("proto: KVPair: illegal tag %d (wire type %d)", fieldNum, wire)
		}
		switch fieldNum {
		case 1:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field Key", wireType)
			}
			var stringLen uint64
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowKv
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				stringLen |= uint64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			intStringLen := int(stringLen)
			if intStringLen < 0 {
				return ErrInvalidLengthKv
			}
			postIndex := iNdEx + intStringLen
			if postIndex < 0 {
				return ErrInvalidLengthKv
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.Key = string(dAtA[iNdEx:postIndex])
			iNdEx = postIndex
		case 2:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field Ikey", wireType)
			}
			m.Ikey = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowKv
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.Ikey |= int64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 3:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field Value", wireType)
			}
			var stringLen uint64
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowKv
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				stringLen |= uint64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			intStringLen := int(stringLen)
			if intStringLen < 0 {
				return ErrInvalidLengthKv
			}
			postIndex := iNdEx + intStringLen
			if postIndex < 0 {
				return ErrInvalidLengthKv
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.Value = string(dAtA[iNdEx:postIndex])
			iNdEx = postIndex
		case 4:
			if wireType != 0 {
				return fmt.Errorf("proto: wrong wireType = %d for field Ivalue", wireType)
			}
			m.Ivalue = 0
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowKv
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				m.Ivalue |= int64(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
		case 5:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field Bvalue", wireType)
			}
			var byteLen int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowKv
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				byteLen |= int(b&0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if byteLen < 0 {
				return ErrInvalidLengthKv
			}
			postIndex := iNdEx + byteLen
			if postIndex < 0 {
				return ErrInvalidLengthKv
			}
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			m.Bvalue = append(m.Bvalue[:0], dAtA[iNdEx:postIndex]...)
			if m.Bvalue == nil {
				m.Bvalue = []byte{}
			}
			iNdEx = postIndex
		default:
			iNdEx = preIndex
			skippy, err := skipKv(dAtA[iNdEx:])
			if err != nil {
				return err
			}
			if (skippy < 0) || (iNdEx+skippy) < 0 {
				return ErrInvalidLengthKv
			}
			if (iNdEx + skippy) > l {
				return io.ErrUnexpectedEOF
			}
			iNdEx += skippy
		}
	}

	if iNdEx > l {
		return io.ErrUnexpectedEOF
	}
	return nil
}
func skipKv(dAtA []byte) (n int, err error) {
	l := len(dAtA)
	iNdEx := 0
	depth := 0
	for iNdEx < l {
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return 0, ErrIntOverflowKv
			}
			if iNdEx >= l {
				return 0, io.ErrUnexpectedEOF
			}
			b := dAtA[iNdEx]
			iNdEx++
			wire |= (uint64(b) & 0x7F) << shift
			if b < 0x80 {
				break
			}
		}
		wireType := int(wire & 0x7)
		switch wireType {
		case 0:
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return 0, ErrIntOverflowKv
				}
				if iNdEx >= l {
					return 0, io.ErrUnexpectedEOF
				}
				iNdEx++
				if dAtA[iNdEx-1] < 0x80 {
					break
				}
			}
		case 1:
			iNdEx += 8
		case 2:
			var length int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return 0, ErrIntOverflowKv
				}
				if iNdEx >= l {
					return 0, io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				length |= (int(b) & 0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if length < 0 {
				return 0, ErrInvalidLengthKv
			}
			iNdEx += length
		case 3:
			depth++
		case 4:
			if depth == 0 {
				return 0, ErrUnexpectedEndOfGroupKv
			}
			depth--
		case 5:
			iNdEx += 4
		default:
			return 0, fmt.Errorf("proto: illegal wireType %d", wireType)
		}
		if iNdEx < 0 {
			return 0, ErrInvalidLengthKv
		}
		if depth == 0 {
			return iNdEx, nil
		}
	}
	return 0, io.ErrUnexpectedEOF
}

var (
	ErrInvalidLengthKv        = fmt.Errorf("proto: negative length found during unmarshaling")
	ErrIntOverflowKv          = fmt.Errorf("proto: integer overflow")
	ErrUnexpectedEndOfGroupKv = fmt.Errorf("proto: unexpected end of group")
)
