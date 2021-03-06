// Code generated by protoc-gen-gogo. DO NOT EDIT.
// source: cockroach/pkg/settings/cluster/cluster_version.proto

/*
	Package cluster is a generated protocol buffer package.

	It is generated from these files:
		cockroach/pkg/settings/cluster/cluster_version.proto

	It has these top-level messages:
		ClusterVersion
*/
package cluster

import proto "github.com/gogo/protobuf/proto"
import fmt "fmt"
import math "math"
import cockroach_roachpb "github.com/cockroachdb/cockroach/pkg/roachpb"

import io "io"

// Reference imports to suppress errors if they are not otherwise used.
var _ = proto.Marshal
var _ = fmt.Errorf
var _ = math.Inf

// This is a compile-time assertion to ensure that this generated file
// is compatible with the proto package it is being compiled against.
// A compilation error at this line likely means your copy of the
// proto package needs to be updated.
const _ = proto.GoGoProtoPackageIsVersion2 // please upgrade the proto package

type ClusterVersion struct {
	// The minimum_version required for any node to support. This
	// value must monotonically increase.
	MinimumVersion cockroach_roachpb.Version `protobuf:"bytes,1,opt,name=minimum_version,json=minimumVersion" json:"minimum_version"`
	// The version of functionality in use in the cluster. Unlike
	// minimum_version, use_version may be downgraded, which will
	// disable functionality requiring a higher version. However,
	// some functionality, once in use, can not be discontinued.
	// Support for that functionality is guaranteed by the ratchet
	// of minimum_version.
	UseVersion cockroach_roachpb.Version `protobuf:"bytes,2,opt,name=use_version,json=useVersion" json:"use_version"`
}

func (m *ClusterVersion) Reset()                    { *m = ClusterVersion{} }
func (m *ClusterVersion) String() string            { return proto.CompactTextString(m) }
func (*ClusterVersion) ProtoMessage()               {}
func (*ClusterVersion) Descriptor() ([]byte, []int) { return fileDescriptorClusterVersion, []int{0} }

func init() {
	proto.RegisterType((*ClusterVersion)(nil), "cockroach.base.ClusterVersion")
}
func (m *ClusterVersion) Marshal() (dAtA []byte, err error) {
	size := m.Size()
	dAtA = make([]byte, size)
	n, err := m.MarshalTo(dAtA)
	if err != nil {
		return nil, err
	}
	return dAtA[:n], nil
}

func (m *ClusterVersion) MarshalTo(dAtA []byte) (int, error) {
	var i int
	_ = i
	var l int
	_ = l
	dAtA[i] = 0xa
	i++
	i = encodeVarintClusterVersion(dAtA, i, uint64(m.MinimumVersion.Size()))
	n1, err := m.MinimumVersion.MarshalTo(dAtA[i:])
	if err != nil {
		return 0, err
	}
	i += n1
	dAtA[i] = 0x12
	i++
	i = encodeVarintClusterVersion(dAtA, i, uint64(m.UseVersion.Size()))
	n2, err := m.UseVersion.MarshalTo(dAtA[i:])
	if err != nil {
		return 0, err
	}
	i += n2
	return i, nil
}

func encodeVarintClusterVersion(dAtA []byte, offset int, v uint64) int {
	for v >= 1<<7 {
		dAtA[offset] = uint8(v&0x7f | 0x80)
		v >>= 7
		offset++
	}
	dAtA[offset] = uint8(v)
	return offset + 1
}
func (m *ClusterVersion) Size() (n int) {
	var l int
	_ = l
	l = m.MinimumVersion.Size()
	n += 1 + l + sovClusterVersion(uint64(l))
	l = m.UseVersion.Size()
	n += 1 + l + sovClusterVersion(uint64(l))
	return n
}

func sovClusterVersion(x uint64) (n int) {
	for {
		n++
		x >>= 7
		if x == 0 {
			break
		}
	}
	return n
}
func sozClusterVersion(x uint64) (n int) {
	return sovClusterVersion(uint64((x << 1) ^ uint64((int64(x) >> 63))))
}
func (m *ClusterVersion) Unmarshal(dAtA []byte) error {
	l := len(dAtA)
	iNdEx := 0
	for iNdEx < l {
		preIndex := iNdEx
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return ErrIntOverflowClusterVersion
			}
			if iNdEx >= l {
				return io.ErrUnexpectedEOF
			}
			b := dAtA[iNdEx]
			iNdEx++
			wire |= (uint64(b) & 0x7F) << shift
			if b < 0x80 {
				break
			}
		}
		fieldNum := int32(wire >> 3)
		wireType := int(wire & 0x7)
		if wireType == 4 {
			return fmt.Errorf("proto: ClusterVersion: wiretype end group for non-group")
		}
		if fieldNum <= 0 {
			return fmt.Errorf("proto: ClusterVersion: illegal tag %d (wire type %d)", fieldNum, wire)
		}
		switch fieldNum {
		case 1:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field MinimumVersion", wireType)
			}
			var msglen int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowClusterVersion
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				msglen |= (int(b) & 0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if msglen < 0 {
				return ErrInvalidLengthClusterVersion
			}
			postIndex := iNdEx + msglen
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			if err := m.MinimumVersion.Unmarshal(dAtA[iNdEx:postIndex]); err != nil {
				return err
			}
			iNdEx = postIndex
		case 2:
			if wireType != 2 {
				return fmt.Errorf("proto: wrong wireType = %d for field UseVersion", wireType)
			}
			var msglen int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return ErrIntOverflowClusterVersion
				}
				if iNdEx >= l {
					return io.ErrUnexpectedEOF
				}
				b := dAtA[iNdEx]
				iNdEx++
				msglen |= (int(b) & 0x7F) << shift
				if b < 0x80 {
					break
				}
			}
			if msglen < 0 {
				return ErrInvalidLengthClusterVersion
			}
			postIndex := iNdEx + msglen
			if postIndex > l {
				return io.ErrUnexpectedEOF
			}
			if err := m.UseVersion.Unmarshal(dAtA[iNdEx:postIndex]); err != nil {
				return err
			}
			iNdEx = postIndex
		default:
			iNdEx = preIndex
			skippy, err := skipClusterVersion(dAtA[iNdEx:])
			if err != nil {
				return err
			}
			if skippy < 0 {
				return ErrInvalidLengthClusterVersion
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
func skipClusterVersion(dAtA []byte) (n int, err error) {
	l := len(dAtA)
	iNdEx := 0
	for iNdEx < l {
		var wire uint64
		for shift := uint(0); ; shift += 7 {
			if shift >= 64 {
				return 0, ErrIntOverflowClusterVersion
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
					return 0, ErrIntOverflowClusterVersion
				}
				if iNdEx >= l {
					return 0, io.ErrUnexpectedEOF
				}
				iNdEx++
				if dAtA[iNdEx-1] < 0x80 {
					break
				}
			}
			return iNdEx, nil
		case 1:
			iNdEx += 8
			return iNdEx, nil
		case 2:
			var length int
			for shift := uint(0); ; shift += 7 {
				if shift >= 64 {
					return 0, ErrIntOverflowClusterVersion
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
			iNdEx += length
			if length < 0 {
				return 0, ErrInvalidLengthClusterVersion
			}
			return iNdEx, nil
		case 3:
			for {
				var innerWire uint64
				var start int = iNdEx
				for shift := uint(0); ; shift += 7 {
					if shift >= 64 {
						return 0, ErrIntOverflowClusterVersion
					}
					if iNdEx >= l {
						return 0, io.ErrUnexpectedEOF
					}
					b := dAtA[iNdEx]
					iNdEx++
					innerWire |= (uint64(b) & 0x7F) << shift
					if b < 0x80 {
						break
					}
				}
				innerWireType := int(innerWire & 0x7)
				if innerWireType == 4 {
					break
				}
				next, err := skipClusterVersion(dAtA[start:])
				if err != nil {
					return 0, err
				}
				iNdEx = start + next
			}
			return iNdEx, nil
		case 4:
			return iNdEx, nil
		case 5:
			iNdEx += 4
			return iNdEx, nil
		default:
			return 0, fmt.Errorf("proto: illegal wireType %d", wireType)
		}
	}
	panic("unreachable")
}

var (
	ErrInvalidLengthClusterVersion = fmt.Errorf("proto: negative length found during unmarshaling")
	ErrIntOverflowClusterVersion   = fmt.Errorf("proto: integer overflow")
)

func init() {
	proto.RegisterFile("cockroach/pkg/settings/cluster/cluster_version.proto", fileDescriptorClusterVersion)
}

var fileDescriptorClusterVersion = []byte{
	// 219 bytes of a gzipped FileDescriptorProto
	0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0xff, 0xe2, 0x32, 0x49, 0xce, 0x4f, 0xce,
	0x2e, 0xca, 0x4f, 0x4c, 0xce, 0xd0, 0x2f, 0xc8, 0x4e, 0xd7, 0x2f, 0x4e, 0x2d, 0x29, 0xc9, 0xcc,
	0x4b, 0x2f, 0xd6, 0x4f, 0xce, 0x29, 0x2d, 0x2e, 0x49, 0x2d, 0x82, 0xd1, 0xf1, 0x65, 0xa9, 0x45,
	0xc5, 0x99, 0xf9, 0x79, 0x7a, 0x05, 0x45, 0xf9, 0x25, 0xf9, 0x42, 0x7c, 0x70, 0x5d, 0x7a, 0x49,
	0x89, 0xc5, 0xa9, 0x52, 0x2a, 0xa8, 0xa6, 0x80, 0x59, 0x05, 0x49, 0xfa, 0xb9, 0xa9, 0x25, 0x89,
	0x29, 0x89, 0x25, 0x89, 0x10, 0x5d, 0x52, 0x22, 0xe9, 0xf9, 0xe9, 0xf9, 0x60, 0xa6, 0x3e, 0x88,
	0x05, 0x11, 0x55, 0x9a, 0xc7, 0xc8, 0xc5, 0xe7, 0x0c, 0xb1, 0x25, 0x0c, 0x62, 0x89, 0x90, 0x27,
	0x17, 0x7f, 0x6e, 0x66, 0x5e, 0x66, 0x6e, 0x69, 0x2e, 0xcc, 0x5e, 0x09, 0x46, 0x05, 0x46, 0x0d,
	0x6e, 0x23, 0x29, 0x3d, 0x84, 0xc5, 0x50, 0x4b, 0xf4, 0xa0, 0x9a, 0x9c, 0x58, 0x4e, 0xdc, 0x93,
	0x67, 0x08, 0xe2, 0x83, 0x6a, 0x84, 0x19, 0xe5, 0xc8, 0xc5, 0x5d, 0x5a, 0x9c, 0x0a, 0x37, 0x86,
	0x89, 0x48, 0x63, 0xb8, 0x4a, 0x8b, 0x53, 0x61, 0x22, 0x8a, 0x27, 0x1e, 0xca, 0x31, 0x9c, 0x78,
	0x24, 0xc7, 0x78, 0xe1, 0x91, 0x1c, 0xe3, 0x8d, 0x47, 0x72, 0x8c, 0x0f, 0x1e, 0xc9, 0x31, 0x4e,
	0x78, 0x2c, 0xc7, 0x10, 0xc5, 0x0e, 0x0d, 0x9d, 0x24, 0x36, 0xb0, 0x57, 0x8c, 0x01, 0x01, 0x00,
	0x00, 0xff, 0xff, 0x19, 0xdf, 0xbe, 0x52, 0x4e, 0x01, 0x00, 0x00,
}
