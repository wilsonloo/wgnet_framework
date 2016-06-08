package wgnet

/**
作者:wilsonloo
模块：tcp服务端接口模块
说明：该框架采用事件驱动机制，创建服务实例时不要绑定回调函数
创建时间：2016-5-29
**/

import (
	"fmt"
	"net"
	"sync"
	"errors"
	"strconv"
)

//NewConnCallback 新连接回调
type NewConnCallback func(*WgTCPConn)

//DisConnCallback 连接断开回调
type DisConnCallback func(*WgTCPConn)

// PacketHandler 消息处理器
type PacketHandler func(*WgTCPConn, *Message) error

// packet 处理失败回调
type PacketHandleFailedHander func(*WgTCPConn, error)

//RawMessageCallback 没有注册的消息的回调
type RawMessageCallback func(conn *WgTCPConn, msg *Message) error

//GxTCPServer tcp服务器
type WgTCPServer struct {
	conn_mutex                   *sync.Mutex           // 处理连接的互斥锁（一般用于增减连接）
	addr_mapped_onns             map[string]*WgTCPConn // 地址映射的 WgTCPConn
	ID_mapped_conns              map[uint32]*WgTCPConn // 连接ID映射的 WgTCPConn
	ConnIDGenerator              uint32                // 用来给客户端连接分配ID
	ConnIDMask                   uint32                // 连接ID生成掩码

	on_new_conn_handler          NewConnCallback // 新连接事件回调
	on_dis_conn_handler          DisConnCallback // 断开连接回调
	raw_message_handler          RawMessageCallback // 原始消息回调

	packet_handlers              map[uint16] PacketHandler // 消息处理事件
	packet_handle_failed_handler PacketHandleFailedHander // 消息处理失败事件
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
	server.raw_message_handler = rm
	server.packet_handlers = make(map[uint16]PacketHandler)

	return server
}

func (server *WgTCPServer) RegisterPacketHandler(cmd uint16, handler PacketHandler) {
	server.packet_handlers[cmd] = handler
}

func (server *WgTCPServer) RegisterPacketHandleFailedHander(handler PacketHandleFailedHander)  {
	server.packet_handle_failed_handler = handler
}

//Start 服务端启动函数
func (server *WgTCPServer) Start(addr string) error {

	// 监听端口
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		// todo logl GxMisc.Debug("lister %s fail", addr)
		return err
	}

	// GxMisc.Debug("server start, host: %s", addr)
	// fmt.Println("WgTCPServer.Start() server start, host: ", addr)

	// 没有一个客户端连接， 就开启协程处理
	go func() {
		for {
			conn, err1 := listener.Accept()
			if err1 != nil {
				fmt.Println("server Accept fail, err: ", err1)
				return
			}

			go server.handle_new_conn(conn)
		}
	}()

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
	server.addr_mapped_onns[gxConn.Remote] = gxConn
}

// runConn 新连接处理函数
func (server *WgTCPServer) handle_new_conn(conn net.Conn) {

	gxConn := NewTCPConn(nil)
	gxConn.Conn = conn
	gxConn.Connected = true
	gxConn.Remote = conn.RemoteAddr().String()

	fmt.Println("new conn:", gxConn.Remote)

	//生成通讯需要的密钥
	// if gxConn.ServerKey() != nil {
	// 	gxConn.Conn.Close()
	// 	continue
	// }

	// 添加新连接
	server.add_new_conn(gxConn)
	server.on_new_conn_handler(gxConn)

	// 心跳检测
	go server.handle_conn_heartbeat(gxConn)

	for {
		// 处理数据接收
		msg, err := gxConn.Recv()
		if err != nil {
			fmt.Printf("EEXXXXEE remote[%s:%s], info: %s\n", "gxConn.M", gxConn.Remote, err.Error())
			server.closeConn(gxConn)
			return
		}

		// 处理消息
		err = server.handle_msg(gxConn, msg)
	}
}

// 处理message
func (server *WgTCPServer) handle_msg(conn *WgTCPConn, msg *Message) error{

	var err error

	// 获取消息处理器
	packet_handler, ok := server.packet_handlers[msg.Cmd()]
	if !ok {
		// 消息没有被注册
		if server.raw_message_handler != nil {
			err = server.raw_message_handler(conn, msg)
		} else {
			err = errors.New("unregister cmd:" + strconv.Itoa(int(msg.Cmd())))
			fmt.Println("UNREGISTER CMD ", msg.Cmd())
		}
	} else {
		//消息已经被注册
		err = packet_handler(conn, msg)
	}

	// 先回收消息
	FreeMessage(msg)

	if err != nil {
		// 最后一次推送给用户
		if server.packet_handle_failed_handler != nil {
			server.packet_handle_failed_handler(conn, err)
		}

		//回调返回值不为空，则关闭连接
		fmt.Println("err001:", err.Error())
		server.closeConn(conn)
	}

	return err
}

/* 处理心跳函数，用协程启动
 */
func (server *WgTCPServer) handle_conn_heartbeat(conn *WgTCPConn) {
	for {
		select {
		case state := <-conn.Toc:

			// 收到系统消息，用以直接退出协程
			if state == EVENT_TO_EXIT {
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

func (server *WgTCPServer) closeConn(conn *WgTCPConn) {
	if !conn.Closed {

		// 标记此次连接已关闭
		conn.Closed = true

		// 回调断开连接处理
		server.on_dis_conn_handler(conn)

		// 从连接池中移除
		server.conn_mutex.Lock()
		server.conn_mutex.Unlock()
		delete(server.addr_mapped_onns, conn.Remote)
		delete(server.ID_mapped_conns, conn.ID)

		conn.Toc <- 0xFFFF

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
