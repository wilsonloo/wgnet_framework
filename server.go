package wgnet

/**
作者:wilsonloo
模块：tcp服务端接口模块
说明：该框架采用事件驱动机制，创建服务实例时不要绑定回调函数
创建时间：2016-5-29
**/

import (
	"net"
	"sync"
	"fmt"

	// todo
	// "github.com/golang/protobuf/proto"
)

/* todo
import (
	"GxFramework/GxMessage"
	"GxFramework/GxMisc"
	"GxFramework/GxStatic"
)
*/

//NewConnCallback 新连接回调
type NewConnCallback func(*WgTCPConn)

//DisConnCallback 连接断开回调
type DisConnCallback func(*WgTCPConn)

// PacketHandler 消息处理器
type PacketHandler func(*WgTCPConn, *Message) error

//RawMessageCallback 没有注册的消息的回调
type RawMessageCallback func(*WgTCPConn, *Message) error

//GxTCPServer tcp服务器
type WgTCPServer struct {
	conn_mutex            *sync.Mutex           // 处理连接的互斥锁（一般用于增减连接）
	addr_mapped_onns      map[string]*WgTCPConn // 地址映射的 WgTCPConn
	ID_mapped_conns       map[uint32]*WgTCPConn // 连接ID映射的 WgTCPConn
	ConnIDGenerator       uint32                // 用来给客户端连接分配ID
	ConnIDMask            uint32                // 连接ID生成掩码

	on_new_conn_handler     NewConnCallback		// 新连接事件回调
	on_dis_conn_handler	DisConnCallback       	// 断开连接回调
	Rm                    RawMessageCallback

	packet_handlers map[uint16]PacketHandler	// 消息处理事件
}

// NewWgTCPServer 生成一个新的 WgTCPServer
func NewWgTCPServer(new_conn_handler NewConnCallback, dis_conn_handler DisConnCallback, rm RawMessageCallback, messageCtrl bool) *WgTCPServer {
	server := new(WgTCPServer)
	server.addr_mapped_onns = make(map[string]*WgTCPConn)
	server.ID_mapped_conns = make(map[uint32]*WgTCPConn)
	server.ConnIDGenerator = 0
	server.conn_mutex = new(sync.Mutex)

	server.on_new_conn_handler = new_conn_handler
	server.on_dis_conn_handler = dis_conn_handler
	server.Rm = rm
	server.packet_handlers = make(map[uint16]PacketHandler)

	// 注册心跳回调
	server.RegisterPacketHandler(PROTOCOL_CMD_HEARTBEAT, HeartbeatCallback)

	return server
}

func (server *WgTCPServer) RegisterPacketHandler(cmd uint16, handler PacketHandler) {
	server.packet_handlers[cmd] = handler
}

//Start 服务端启动函数
func (server *WgTCPServer) Start(port string) error {

	// 监听端口
	listener, err := net.Listen("tcp", port)
	if err != nil {
		// todo logl GxMisc.Debug("lister %s fail", port)
		return err
	}

	// GxMisc.Debug("server start, host: %s", port)
	fmt.Println("server start, host: ", port)

	// 没有一个客户端连接， 就开启协程处理
	for {
		conn, err1 := listener.Accept()
		if err1 != nil {
			// todo log GxMisc.Debug("server Accept fail, err: ", err1)
			return err1
		}

		go server.handle_new_conn(conn)
	}

	return nil
}

// 分配新ID
func (server *WgTCPServer) add_new_conn(gxConn *WgTCPConn) {

	server.conn_mutex.Lock()
	defer server.conn_mutex.Unlock()

	//分配ID，一般ID用三个字节保存就可以，第四个字节保留
	for {
		server.ConnIDGenerator++

		// 检测该ID 是否已经分配了
		if _, ok := server.ID_mapped_conns[server.ConnIDGenerator]; !ok {
			break
		}
	}
	gxConn.ID = server.ConnIDGenerator
	// gxConn.M = "Cli"

	server.ID_mapped_conns[gxConn.ID] = gxConn
	server.addr_mapped_onns [gxConn.Remote] = gxConn
}

// runConn 新连接处理函数
func (server *WgTCPServer) handle_new_conn(conn net.Conn) {
	gxConn := NewTCPConn()
	gxConn.Conn = conn
	gxConn.Connected = true
	gxConn.Remote = conn.RemoteAddr().String()

	//生成通讯需要的密钥
	// if gxConn.ServerKey() != nil {
	// 	gxConn.Conn.Close()
	// 	continue
	// }

	// 添加新连接
	server.add_new_conn(gxConn)
	server.on_new_conn_handler(gxConn)

	// 心跳检测
	go gxConn.runHeartbeat(server)

	for {
		// 处理数据接收
		msg, err := gxConn.Recv()
		if err != nil {
			//GxMisc.Error("EEXXXXEE remote[%s:%s], info: %s", gxConn.M, gxConn.Remote, err)
			server.closeConn(gxConn)
			return
		}

		if msg.Cmd() != PROTOCOL_CMD_HEARTBEAT {
			// TODO LOG GxMisc.Debug("<<==== remote[%s:%s], info: %s", gxConn.M, gxConn.Remote, msg.String())
		}

		// 获取消息处理器
		packet_handler, ok := server.packet_handlers[msg.Cmd()]
		if !ok {
			// 消息没有被注册
			// err = server.Rm(gxConn, msg)
			fmt.Println("UNREGISTER CMD ", msg.Cmd())

			FreeMessage(msg)
			continue
			/*if err != nil {
				server.closeConn(gxConn)
				return
			}*/
		}

		//消息已经被注册
		err = packet_handler(gxConn, msg)

		// 先回收消息
		FreeMessage(msg)

		if err != nil {
			//回调返回值不为空，则关闭连接
			server.closeConn(gxConn)
			return
		}
	}
}

func (server *WgTCPServer) closeConn(conn *WgTCPConn) {
	if !conn.Close {

		// 标记此次连接已关闭
		conn.Close = true

		// 回调断开连接处理
		server.on_dis_conn_handler(conn)

		// 从连接池中移除
		server.conn_mutex.Lock()
		server.conn_mutex.Unlock()
		delete(server.addr_mapped_onns, conn.Remote)
		delete(server.ID_mapped_conns, conn.ID)

		// todo conn.Toc <- 0xFFFF

		// 调用实际的断开连接
		conn.Conn.Close()
	}
}

//ConnectCount 返回当前连接数量
func (server *WgTCPServer) ConnectCount() int {
	server.conn_mutex.Lock()
	defer server.conn_mutex.Unlock()

	return len(server.ID_mapped_conns)
}

//FindConnByRetome 根据连接地址返回连接指针
func (server *WgTCPServer) FindConnByRetome(retome string) *WgTCPConn {
	server.conn_mutex.Lock()
	defer server.conn_mutex.Unlock()

	info, ok := server.addr_mapped_onns[retome]
	if ok {
		return info
	}
	return nil
}

//FindConnByID 根据连接ID返回连接指针
func (server *WgTCPServer) FindConnByID(ID uint32) *WgTCPConn {
	server.conn_mutex.Lock()
	defer server.conn_mutex.Unlock()

	info, ok := server.ID_mapped_conns[ID]
	if ok {
		return info
	}
	return nil
}

//HeartbeatCallback 心跳回调
func HeartbeatCallback(conn *WgTCPConn, msg *Message) error {
	/* todo conn.Toc <- 0
	msg.SetID(conn.ID)
	msg.SetRet(GxStatic.RetSucc)
	conn.Send(msg)
	*/

	return nil
}

/*
//SendRawMessage 发送一个字符串消息
func SendRawMessage(conn *GxTCPConn, mask uint16, ID uint32, cmd uint16, seq uint16, ret uint16, buff []byte) {
	msg := GxMessage.GetGxMessage()
	defer GxMessage.FreeMessage(msg)

	msg.SetID(ID)
	msg.SetCmd(cmd)
	msg.SetRet(ret)
	msg.SetSeq(seq)
	msg.SetMask(mask)

	if len(buff) == 0 {
		msg.SetLen(0)
	} else {
		err := msg.Package(buff)
		if err != nil {
			GxMisc.Debug("PackagePbmsg error")
			return
		}
	}

	conn.Send(msg)
}

//SendPbMessage 发送一个pb消息
func SendPbMessage(conn *GxTCPConn, mask uint16, ID uint32, cmd uint16, seq uint16, ret uint16, pb proto.Message) {
	msg := GxMessage.GetGxMessage()
	defer GxMessage.FreeMessage(msg)

	msg.SetID(ID)
	msg.SetCmd(cmd)
	msg.SetRet(ret)
	msg.SetSeq(seq)
	msg.SetMask(mask)

	if pb == nil {
		msg.SetLen(0)
	} else {
		err := msg.PackagePbmsg(pb)
		if err != nil {
			GxMisc.Debug("PackagePbmsg error")
			return
		}
	}

	if msg.GetCmd() != GxStatic.CmdHeartbeat {
		if pb == nil {
			GxMisc.Debug("====>> remote[%s:%s], info: %s", conn.M, conn.Remote, msg.String())
		} else {
			GxMisc.Debug("====>> remote[%s:%s], info: %s, rsp: \r\n\t%s", conn.M, conn.Remote, msg.String(), pb.String())
		}
	}
	conn.Send(msg)
}
*/