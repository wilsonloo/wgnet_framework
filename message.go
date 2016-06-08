package wgnet

/**
作者:wilsonloo
模块：tcp连接的 消息
说明：消息采用 消息头 + 数据 的组织方式，其中消息头是一个数组，其长度可自定义 MESSAGE_HEADER_LEN
创建时间：2016-5-29
**/

import (
	proto "github.com/golang/protobuf/proto"
)

// 定义
const (
	PACKET_HEADER_LEN   = 4    // 消息头的长度
	MAX_PACKET_DATA_LEN = 1024 // 最大消息长度
)

// todo 此部分可修改
// 消息头声明（注：该结构体只是用以说明，不会被使用）
type _PacketHeader struct {
	len uint16 // 长度
	cmd uint16 // 命令号
}

// todo 此部分可修改
// 消息头声明（注：该结构体只是用以说明，不会被使用）
type _PacketData struct {
	holder uint32 // 占位符
}

// 消息定义
type Message struct {
	Header []byte // 消息头，
	Data   []byte // 实际消息
}

// 获取整体消息大小
func (msg *Message) MessageTotalSize() uint32{
	return uint32(PACKET_HEADER_LEN) + uint32(msg.PacketLen())
}

// todo 此部分可修改
func GetPacketLen(header []byte) uint16 {
	return uint16(header[0]) <<8 | uint16(header[1])
}

// 获取消息长度
func (msg *Message) PacketLen() uint16 {
	return GetPacketLen(msg.Header)
}

// todo 此部分可修改
// 设置消息长度
func (msg *Message) SetPacketLen(len uint16) {
	msg.Header[0] = byte((len >> 8) & 0xFF)
	msg.Header[1] = byte(len & 0xFF)
}

// todo 此部分可修改
// 重置消息
func (msg *Message) ResetPacket() {
	// todo 需要放回池内
	msg.Data = nil

	msg.Header[0] = 0
	msg.Header[1] = 0
}

// todo 此部分可修改
func GetCMD(header []byte) uint16 {
	return uint16(header[2]) <<8 | uint16(header[3])
}
// 获取消息命令号
func (msg *Message) Cmd() uint16 {
	return GetCMD(msg.Header)
}

func (msg *Message) SetCmd(cmd uint16) {
	msg.Header[2] = byte((cmd >> 8) & 0xFF)
	msg.Header[3] = byte(cmd & 0xFF)
}

func (msg *Message) InitData() {
	// todo 需要从池内获取
	msg.Data = make([]byte, msg.PacketLen())
}

func(msg *Message) Dump() []byte {
	ret := make([]byte, msg.MessageTotalSize())
	copy(ret[0:], msg.Header[:])
	copy(ret[PACKET_HEADER_LEN:], msg.Data[:])

	return ret
}

//UnpackagePbmsg 解包protobuf消息
func (msg *Message) Unpackage2Pbmsg(pb proto.Message) error {
	return proto.Unmarshal(msg.Data, pb)
}

//Package 打包原生字符串
// @param raw_len 返回原始数据的长度
func (msg *Message) Package(cmd uint16, buff []byte) (packeted_len uint32, err error) {
	size := len(buff)
	if size == 0 {
		return 0, nil
	}

	msg.ResetPacket()
	msg.SetPacketLen(uint16(size))
	msg.InitData()

	// 先写
	copy(msg.Data[:], buff)
	return uint32(size), nil
}

// 按照消息长度初始化 消息体
func (msg *Message) PreparePacket() {
	// todo 优化到从缓冲池读取数据
	msg.Data = make([]byte, msg.PacketLen())
}

func MakeHeader() []byte {
	return make([]byte, PACKET_HEADER_LEN)
}

/* 创建一个消息
@param data_size 预设的消息长度
*/
func NewMessage() *Message {
	// todo 优化：采用 message pool 的方式

	msg := new(Message)

	// 固定消息头长度
	msg.Header = MakeHeader()

	return msg
}

func FreeMessage(msg *Message) {
	// todo 进行回收到消息池
}