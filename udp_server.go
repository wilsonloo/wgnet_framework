package wgnet

/**
作者:wilsonloo
模块：udp服务端接口模块
说明：该框架采用事件驱动机制，创建服务实例时不要绑定回调函数
创建时间：2016-5-29
**/

import (
	"fmt"
	"net"
	"errors"
	proto "github.com/golang/protobuf/proto"
)

//NewConnCallback 新连接回调
type NewUDPConnCallback func(remote *net.UDPAddr)

// PacketHandler 消息处理器
type UDPPacketHandler func(remote *net.UDPAddr, msg *Message) error

// packet 处理失败回调
type UDPPacketHandleFailedHander func(remote *net.UDPAddr, err error)

//RawMessageCallback 没有注册的消息的回调
type UDPRawMessageCallback func(*Message) error

//GxTCPServer tcp服务器
type UDPServer struct {

	ConnIDMask       uint32                // 连接ID生成掩码
	LocalAddr 	net.Addr 			// 本地地址
	on_new_conn_handler NewUDPConnCallback // 新连接事件回调
	Rm                  UDPRawMessageCallback

	packet_handlers map[uint16] UDPPacketHandler // 消息处理事件
	packet_handle_failed_handler UDPPacketHandleFailedHander // 消息处理失败事件
}

// NewUDPServer 生成一个新的 UDPServer
func NewUDPServer(new_conn_handler NewUDPConnCallback, rm UDPRawMessageCallback, fh UDPPacketHandleFailedHander, messageCtrl bool) *UDPServer {
	server := new(UDPServer)

	server.on_new_conn_handler = new_conn_handler
	server.packet_handle_failed_handler = fh
	server.Rm = rm
	server.packet_handlers = make(map[uint16]UDPPacketHandler)

	return server
}

func (server *UDPServer) RegisterPacketHandler(cmd uint16, handler UDPPacketHandler) {
	server.packet_handlers[cmd] = handler
}

func (server *UDPServer) RegisterPacketHandleFailedHander(handler UDPPacketHandleFailedHander)  {
	server.packet_handle_failed_handler = handler
}

//Start 服务端启动函数
func (server *UDPServer) Start(addr string) error {

	// 监听端口
	udpAddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return err
	}

	listener, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		// todo logl GxMisc.Debug("lister %s fail", addr)
		return err
	}
	server.LocalAddr = listener.LocalAddr()

	// GxMisc.Debug("server start, host: %s", addr)
	// fmt.Println("UDPServer.Start() server start, host: ", addr)

	// 每有一个客户端连接， 就开启协程处理
	go func() {
		for {
			buf := make([]byte, 1024)
			rlen, remote, err1 := listener.ReadFromUDP(buf)
			if err1 != nil {
				fmt.Println("server Accept fail, err: ", err1)
				return
			}

			// fmt.Println("connected from ", remote.String())

			go server.handle_new_conn(remote, buf, rlen)
		}
	}()

	return nil
}


// Recv 接受消息函数
func (conn *UDPServer) Recv(data[]byte, pos int, last int) (msg *Message, handled_size int, err error) {

	pos_bak := pos
	// 剩余数据量
	remain_data_size := last - pos

	// 检测是否构成一个包头
	if remain_data_size < PACKET_HEADER_LEN {
		// fmt.Println("not enough packet data")
		return nil, 0, errors.New("not enough packet data")
	}

	//写消息头
	//如果读取消息失败，消息要归还给消息池
	msg = NewMessage()
	read_len := copy(msg.Header, data[pos:])
	pos += PACKET_HEADER_LEN

	if uint16(read_len) != PACKET_HEADER_LEN {
		// todo GxMisc.Error("XXXX %s remote[%s:%s] header lenght err: %d != %d", GxStatic.CmdString[msg.GetCmd()], conn.M, conn.Remote, read_len, GxMessage.MessageHeaderLen)
		FreeMessage(msg)
		return nil, 0, errors.New("not enough packet data")
	}

	// 获取消息数据的长度
	packet_len := msg.PacketLen()
	if packet_len == 0 {
		return msg, pos - pos_bak, nil
	}

	//TODO lwj 消息长度异常
	if packet_len > MAX_PACKET_DATA_LEN {
		// TODO LOG GxMisc.Warn("====XXXX %s remote[%s:%s] msg lenght is big:%v", GxStatic.CmdString[msg.GetCmd()], conn.M, conn.Remote, msg)
		FreeMessage(msg)
		return nil, 0, errors.New("max packet len")
	}

	// 写消息体
	msg.PreparePacket()

	// 拷贝所有数据
	read_len = copy(msg.Data[0:], data[pos:])
	if read_len != int(packet_len) {
		fmt.Println("reading not enough data")
		FreeMessage(msg)
		return nil, 0, errors.New("packet data error.")
	}

	// 必须整整一个消息
	if read_len != int(msg.PacketLen()) {
		FreeMessage(msg)
		return nil, 0, errors.New("packet len reading error.")
	}
	pos += read_len

	return msg, pos - pos_bak, nil
}

// runConn 新连接处理函数
func (server *UDPServer) handle_new_conn(remote *net.UDPAddr, data []byte, data_size int) {

	// fmt.Println("new udp conn:", remote.String())

	//生成通讯需要的密钥
	// if gxConn.ServerKey() != nil {
	// 	gxConn.Conn.Close()
	// 	continue
	// }

	// 添加新连接
	server.on_new_conn_handler(remote)

	pos := 0
	for {
		msg, handled_packet_len, err := server.Recv(data, pos, data_size)
		if err != nil {
			// fmt.Println("failed to handle packet:", err)
			return
		}
		pos += handled_packet_len

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
		err = packet_handler(remote, msg)

		// 先回收消息
		FreeMessage(msg)

		if err != nil {
			// 最后一次推送给用户
			if server.packet_handle_failed_handler != nil {
				server.packet_handle_failed_handler(remote, err)
			}

			//回调返回值不为空，则关闭连接
			return
		}
	}
}

// 发送UDP消息
func SendUdpMessage(addr string, msg *Message, msg_len uint32)  error {

	fmt.Println("the udp addr is:", addr)
	udpAddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return err
	}

	fmt.Println("dialing:", addr)
	player_conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		fmt.Println("net.DialUDP fail.", err)
		return err
	}
    	defer player_conn.Close()
	fmt.Println("dialing:", addr, "OK")

	buf := make([]byte, msg_len)
	copy(buf[0:], msg.Header)
	copy(buf[PACKET_HEADER_LEN:], msg.Data)
	if _, err = player_conn.Write(buf); err != nil {
		return err
	}

	return nil
}

// 发送UDP消息
func SendUdpPbMessage(addr string, cmd uint16, pbmsg proto.Message)  error {
fmt.Println(444444)
	// 创建网络消息体
	msg := NewMessage()
	msg.SetCmd(cmd)

	// 将pb序列化
	packet, err := proto.Marshal(pbmsg)
	if err != nil {
		return err
	}

	packet_len, err := msg.Package(cmd, packet)
	if err != nil {
		return err
	}

	if packet_len != uint32(len(packet) ){
		fmt.Printf("packet len error, got %d expecting %d", packet_len, len(packet))
		return errors.New("packet len error")
	}

	return SendUdpMessage(addr, msg, PACKET_HEADER_LEN + packet_len)
}