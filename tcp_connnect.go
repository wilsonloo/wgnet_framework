package wgnet

/**
作者:wilsonloo
模块：tcp连接的 连接实体
说明：
创建时间：2016-5-29
**/

import (
	"errors"
	"fmt"
	"github.com/golang/protobuf/proto"
	"net"
	"sync"
	"time"
)

// tcp连接
type WgTCPConn struct {
	ID        uint32   // 连接ID
	Conn      net.Conn // 实际连接
	Connected bool     // 连接状态（和Close的意义不同，此处仅仅标记是否已连接，和是否断开需要删除无关）

	TimeoutCount  int          // 超时次数
	TimeoutTicker *time.Ticker // 超时检测定时器
	Toc           chan int     // 系统事件

	Remote string // 对端地址

	sendMutex *sync.Mutex // 发送锁
	Close     bool        // 是否已经关闭

	packet_handlers  map[uint16] PacketHandler // 消息处理事件
	raw_message_handler          RawMessageCallback // 原始消息回调
	on_dis_conn_handler          DisConnCallback // 断开连接回调
	packet_handle_failed_handler PacketHandleFailedHander // 消息处理失败事件
}

// 生成一个新的 WgTCPConn
func NewTCPConn(raw_msg_hander RawMessageCallback) *WgTCPConn {
	tcpConn := new(WgTCPConn)
	tcpConn.Connected = false
	tcpConn.TimeoutCount = 0
	tcpConn.Toc = make(chan int, 1)
	tcpConn.TimeoutTicker = time.NewTicker(5 * time.Second)

	// todo tcpConn.M = "Cli" //默认
	tcpConn.sendMutex = new(sync.Mutex)
	tcpConn.Close = false
	tcpConn.raw_message_handler = raw_msg_hander
	tcpConn.packet_handlers = make(map[uint16] PacketHandler)

	return tcpConn
}

// 设置心跳秒数
func (conn *WgTCPConn) SetKeepalive(timeout_seconds int) {
	conn.TimeoutTicker = time.NewTicker(time.Duration(timeout_seconds) * time.Second)
}

func (server *WgTCPConn) RegisterPacketHandler(cmd uint16, handler PacketHandler) {
	server.packet_handlers[cmd] = handler
}

// 发送pb消息
func (conn *WgTCPConn) SendPbMessage(cmd uint16, pbmsg proto.Message) error {

	msg, packeted_len, err := conn.GenMessage(cmd, pbmsg)
	if err != nil {
		return err
	}

	return conn.send_essage(msg, packeted_len)
}

func (conn *WgTCPConn) FreeMessage(msg *Message) {
	FreeMessage(msg)
}

func (conn *WgTCPConn) GenMessage(cmd uint16, pbmsg proto.Message) (*Message, uint32, error) {
		// 创建网络消息体
	msg := NewMessage()
	msg.SetCmd(cmd)

	// 将pb序列化
	packet, err := proto.Marshal(pbmsg)
	if err != nil {
		return nil, 0, err
	}

	packeted_len, err := msg.Package(cmd, packet)
	if err != nil {
		return nil, 0, err
	}

	return msg, packeted_len, nil
}

// Send 发送消息函数
func (conn *WgTCPConn) SendMessage(msg *Message) error {
	return conn.send_essage(msg, uint32(msg.PacketLen()))
}

// Send 发送消息函数
func (conn *WgTCPConn) send_essage(msg *Message, packeted_len uint32) error {
	conn.sendMutex.Lock()
	defer conn.sendMutex.Unlock()

	// 检测连接状态
	if !conn.Connected {
		// todo log
		return errors.New(fmt.Sprintf("remote[Conn:%s] disconnect,send msg:%s fail", conn.Remote, msg))
	}

	var err error

	// 先发送消息头
	if err = conn.send_data(&msg.Header, PACKET_HEADER_LEN); err != nil {
		return err
	}

	// 再发送消息体
	if err = conn.send_data(&msg.Data, packeted_len); err != nil {
		return err
	}

	return nil
}

func (conn *WgTCPConn) SendData(data *[]byte, data_size uint32) error {
	return conn.send_data(data, data_size)
}

func (conn *WgTCPConn) send_data(data *[]byte, data_size uint32) error {

	len, err := conn.Conn.Write(*data)
	if err != nil {

		// 标记为断开（删除操作不应该有这里进行，而是有框架处理，例如心跳检测）
		conn.Connected = false

		// todo log GxMisc.Error("XXXX %s remote[%s:%s] write data err: %d", GxStatic.CmdString[msg.GetCmd()], conn.M, conn.Remote, len)
		return err
	}

	// 检测是否发送完全部数据
	if uint32(len) != data_size {

		// 标记为断开（删除操作不应该有这里进行，而是有框架处理，例如心跳检测）
		conn.Connected = false

		// todo log GxMisc.Error("XXXX %s remote[%s:%s] data lenght err: %d != %d", GxStatic.CmdString[msg.GetCmd()], conn.M, conn.Remote, len, msg.GetLen())
		return errors.New("send error as len not matched")
	}

	// todo log GxMisc.Error("XXXX %s remote[%s:%s] write data ok:%d", GxStatic.CmdString[msg.GetCmd()], conn.M, conn.Remote, len)
	return nil
}

// Recv 接受消息函数
func (conn *WgTCPConn) RecvDataPacket(data []byte, data_size uint32) (*Message, error) {

	if data_size < PACKET_HEADER_LEN {
		return nil, errors.New("invalid message header")
	}

	// 先处理消息头
	// 如果读取消息失败，消息要归还给消息池
	msg := NewMessage()
	read_len := copy(msg.Header[:], data[0:])
	if uint16(read_len) != PACKET_HEADER_LEN {
		FreeMessage(msg)
		return nil, errors.New("recv-message error")
	}

	// 获取消息数据的长度
	packet_len := msg.PacketLen()
	if packet_len > 0 {
		if uint32(packet_len) < data_size - PACKET_HEADER_LEN {
			FreeMessage(msg)
			return nil, errors.New("invalid packet")
		}

		if packet_len > MAX_PACKET_DATA_LEN {
			FreeMessage(msg)
			return nil, errors.New("packet length error.")
		}
	} // else {
		// 有些消息只有消息头
	// }

	//写消息体
	msg.PreparePacket()

	// 阻塞式写满packet数据
	read_len = copy(msg.Data[0:], data[PACKET_HEADER_LEN:])

	// 必须整整一个消息
	if read_len != int(packet_len) {
		FreeMessage(msg)
		return nil, errors.New("packet len reading error.")
	}

	return msg, nil
}

func (gxConn *WgTCPConn) RunRemoteServerService() {
	for {
		// 处理数据接收
		msg, err := gxConn.Recv()
		if err != nil {
			fmt.Printf("EEXXXXEE remote[%s:%s], info: %s\n", "gxConn.M", gxConn.Remote, err.Error())
			gxConn.close()
			return
		}

		// 处理消息
		err = gxConn.handle_msg(msg)
	}
}

func (conn *WgTCPConn) close() {
	if conn.Close {
		return
	}

	// 标记此次连接已关闭
	conn.Close = true

	// 回调断开连接处理
	conn.Toc <- 0xFFFF

	if conn.on_dis_conn_handler != nil {
		conn.on_dis_conn_handler(conn)
	}

	// 调用实际的断开连接
	conn.Conn.Close()
}

// 处理message
func (conn *WgTCPConn) handle_msg(msg *Message) error{

	var err error

	// 获取消息处理器
	packet_handler, ok := conn.packet_handlers[msg.Cmd()]
	if !ok {
		// 消息没有被注册
		if conn.raw_message_handler != nil {
			err = conn.raw_message_handler(conn, msg)
		} else {
			fmt.Println("UNREGISTER CMD ", msg.Cmd())
		}

		err = nil
	} else {
		//消息已经被注册
		err = packet_handler(conn, msg)
	}

	// 先回收消息
	FreeMessage(msg)

	if err != nil {
		// 最后一次推送给用户
		if conn.packet_handle_failed_handler != nil {
			conn.packet_handle_failed_handler(conn, err)
		}

		//回调返回值不为空，则关闭连接
		fmt.Println("err001:", err.Error())
		conn.close()
	}

	return err
}

// Recv 接受消息函数
func (conn *WgTCPConn) Recv() (*Message, error) {

	// 检测连接状态
	if !conn.Connected || conn.Close {
		return nil, errors.New(fmt.Sprintf("remote[Conn:%s] disconnect,recv msg fail", conn.Remote))
	}

	//写消息头
	//如果读取消息失败，消息要归还给消息池
	msg := NewMessage()
	read_len, err := conn.Conn.Read(msg.Header)
	if err != nil {
		// todo log GxMisc.Error("XXXX %s remote[%s:%s] read header err: %d,%s ", GxStatic.CmdString[msg.GetCmd()], conn.M, conn.Remote, read_len, err)
		FreeMessage(msg)
		conn.Connected = false
		return nil, err
	}

	if uint16(read_len) != PACKET_HEADER_LEN {
		// todo GxMisc.Error("XXXX %s remote[%s:%s] header lenght err: %d != %d", GxStatic.CmdString[msg.GetCmd()], conn.M, conn.Remote, read_len, GxMessage.MessageHeaderLen)
		FreeMessage(msg)
		return nil, errors.New("recv error")
	}

	/*
		if err = msg.CheckFormat(); err != nil {
			GxMisc.Error("XXXX %s remote[%s:%s] format err: %d", GxStatic.CmdString[msg.GetCmd()], conn.M, conn.Remote, msg.GetLen())
			GxMessage.FreeMessage(msg)
			return nil, err
		}*/

	// 获取消息数据的长度
	packet_len := msg.PacketLen()
	if packet_len == 0 {
		return msg, nil
	}

	//TODO lwj 消息长度异常
	if packet_len > MAX_PACKET_DATA_LEN {
		// TODO LOG GxMisc.Warn("====XXXX %s remote[%s:%s] msg lenght is big:%v", GxStatic.CmdString[msg.GetCmd()], conn.M, conn.Remote, msg)
		FreeMessage(msg)
		return nil, errors.New("packet length error.")
	}

	//写消息体
	msg.PreparePacket()

	// 阻塞式写满packet数据
	read_len, err = conn.Conn.Read(msg.Data[0:])

	// 检测错误
	if err != nil {
		/* if err != io.EOF {
			return nil, err
		}*/
		FreeMessage(msg)
		return nil, err
	}

	// 必须整整一个消息
	if read_len != int(msg.PacketLen()) {
		FreeMessage(msg)
		return nil, errors.New("packet len reading error.")
	}

	return msg, nil
}

// Connect 连接指定host
func (conn *WgTCPConn) Connect(host string) error {
	c, err := net.Dial("tcp", host)

	if err != nil {
		return err
	}

	fmt.Println("connected to host: ", host)

	conn.Conn = c
	conn.Connected = true
	conn.Remote = c.RemoteAddr().String()
	return nil
}
