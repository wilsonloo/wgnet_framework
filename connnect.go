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
	"net"
	"sync"
	"time"
)

/* todo
import (
	"GxFramework/GxMessage"
	"GxFramework/GxMisc"
	"GxFramework/GxStatic"
	//"encoding/hex"
	"sync"
)
*/

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
}

// 生成一个新的 WgTCPConn
func NewTCPConn() *WgTCPConn {
	tcpConn := new(WgTCPConn)
	tcpConn.Connected = false
	tcpConn.TimeoutCount = 0
	tcpConn.Toc = make(chan int, 1)
	tcpConn.TimeoutTicker = time.NewTicker(1 * time.Second)
	// todo tcpConn.M = "Cli" //默认
	tcpConn.sendMutex = new(sync.Mutex)
	tcpConn.Close = false
	return tcpConn
}

/* 处理心跳函数，用协程启动
@param server 服务器连接
*/
func (conn *WgTCPConn) runHeartbeat(server *WgTCPServer) {
	for {
		select {
		case state := <-conn.Toc:
			// 收到系统消息，用以直接退出协程
			if state == 0XFFFF {
				return
			}
			conn.TimeoutCount = state

		case <-conn.TimeoutTicker.C:
			// 连接超时了

			// 如果超时次数大于3次，直接断开
			if conn.TimeoutCount > 3 {
				fmt.Printf("<====== client[%d] %s:%s timeout\n", conn.ID, "conn.M", conn.Remote)

				// 由server 统一处理断开操作
				server.closeConn(conn)

			} else if conn.TimeoutCount >= 0 {
				conn.TimeoutCount = conn.TimeoutCount + 1
			} else {
				break
			}
		}
	}
}

// Send 发送消息函数
func (conn *WgTCPConn) Send(msg []byte, msg_size int) error {
	conn.sendMutex.Lock()
	defer conn.sendMutex.Unlock()

	// 检测连接状态
	if !conn.Connected {
		// todo log
		return errors.New(fmt.Sprintf("remote[Conn:%s] disconnect,send msg:%s fail", conn.Remote, msg))
	}

	// 执行实际发送
	len, err := conn.Conn.Write(msg)
	if err != nil {

		// 标记为断开（删除操作不应该有这里进行，而是有框架处理，例如心跳检测）
		conn.Connected = false

		// todo log GxMisc.Error("XXXX %s remote[%s:%s] write data err: %d", GxStatic.CmdString[msg.GetCmd()], conn.M, conn.Remote, len)
		return err
	}

	// 检测是否发送完全部数据
	if len != msg_size {

		// 标记为断开（删除操作不应该有这里进行，而是有框架处理，例如心跳检测）
		conn.Connected = false

		// todo log GxMisc.Error("XXXX %s remote[%s:%s] data lenght err: %d != %d", GxStatic.CmdString[msg.GetCmd()], conn.M, conn.Remote, len, msg.GetLen())
		return errors.New("send error as len not matched")
	}

	// todo log GxMisc.Error("XXXX %s remote[%s:%s] write data ok:%d", GxStatic.CmdString[msg.GetCmd()], conn.M, conn.Remote, len)
	return nil
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
		return nil, err
	}

	// 必须整整一个消息
	if read_len != int(msg.PacketLen()) {
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
