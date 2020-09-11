package nsqlookupd

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nsqio/nsq/internal/protocol"
	"github.com/nsqio/nsq/internal/version"
)

type LookupProtocolV1 struct {
	ctx *Context
}

func (p *LookupProtocolV1) IOLoop(conn net.Conn) error {
	var err error
	var line string

	//实例化一个client代表nsqd连接conn
	client := NewClientV1(conn)
	reader := bufio.NewReader(client)
	for {
		//按行读取
		line, err = reader.ReadString('\n')
		if err != nil {
			break
		}

		//去掉空格
		line = strings.TrimSpace(line)
		params := strings.Split(line, " ")

		var response []byte
		//根据params[0]判断是哪种事件 PING/IDENTIFY/REGISTER/UNREGISTER
		response, err = p.Exec(client, reader, params)
		if err != nil {
			//其他事件都返回错误
			ctx := ""
			if parentErr := err.(protocol.ChildErr).Parent(); parentErr != nil {
				ctx = " - " + parentErr.Error()
			}
			p.ctx.nsqlookupd.logf(LOG_ERROR, "[%s] - %s%s", client, err, ctx)

			_, sendErr := protocol.SendResponse(client, []byte(err.Error()))
			if sendErr != nil {
				p.ctx.nsqlookupd.logf(LOG_ERROR, "[%s] - %s%s", client, sendErr, ctx)
				break
			}

			// errors of type FatalClientErr should forceably close the connection
			if _, ok := err.(*protocol.FatalClientErr); ok {
				break
			}
			continue
		}

		//返回数据
		if response != nil {
			_, err = protocol.SendResponse(client, response)
			if err != nil {
				break
			}
		}
	}

	conn.Close()
	p.ctx.nsqlookupd.logf(LOG_INFO, "CLIENT(%s): closing", client)
	if client.peerInfo != nil {
		//删除nsqd的节点信息
		registrations := p.ctx.nsqlookupd.DB.LookupRegistrations(client.peerInfo.id)
		for _, r := range registrations {
			if removed, _ := p.ctx.nsqlookupd.DB.RemoveProducer(r, client.peerInfo.id); removed {
				p.ctx.nsqlookupd.logf(LOG_INFO, "DB: client(%s) UNREGISTER category:%s key:%s subkey:%s",
					client, r.Category, r.Key, r.SubKey)
			}
		}
	}
	return err
}

func (p *LookupProtocolV1) Exec(client *ClientV1, reader *bufio.Reader, params []string) ([]byte, error) {
	switch params[0] {
	case "PING":
		//nsqd ping-pong 心跳
		return p.PING(client, params)
	case "IDENTIFY":
		//nsqd首次连接鉴权,保存nsqd信息
		return p.IDENTIFY(client, reader, params[1:])
	case "REGISTER":
		//注册nsqd
		return p.REGISTER(client, reader, params[1:])
	case "UNREGISTER":
		//注销nsqd
		return p.UNREGISTER(client, reader, params[1:])
	}
	return nil, protocol.NewFatalClientErr(nil, "E_INVALID", fmt.Sprintf("invalid command %s", params[0]))
}

//判断topic channel 命名是否符合
func getTopicChan(command string, params []string) (string, string, error) {
	if len(params) == 0 {
		return "", "", protocol.NewFatalClientErr(nil, "E_INVALID", fmt.Sprintf("%s insufficient number of params", command))
	}

	topicName := params[0]
	var channelName string
	if len(params) >= 2 {
		channelName = params[1]
	}

	//判断topic channel 命名是否符合
	if !protocol.IsValidTopicName(topicName) {
		return "", "", protocol.NewFatalClientErr(nil, "E_BAD_TOPIC", fmt.Sprintf("%s topic name '%s' is not valid", command, topicName))
	}

	if channelName != "" && !protocol.IsValidChannelName(channelName) {
		return "", "", protocol.NewFatalClientErr(nil, "E_BAD_CHANNEL", fmt.Sprintf("%s channel name '%s' is not valid", command, channelName))
	}

	return topicName, channelName, nil
}

/**
1.判断是否已鉴权获取nsqd信息
2.获取topic channel
3.绑定nsqd和topic channel的关系
4.返回ok给nsqd
 */
//REGISTER nsqd向nsqlookup注册自身
func (p *LookupProtocolV1) REGISTER(client *ClientV1, reader *bufio.Reader, params []string) ([]byte, error) {
	//未鉴权获取nsqd信息?
	if client.peerInfo == nil {
		return nil, protocol.NewFatalClientErr(nil, "E_INVALID", "client must IDENTIFY")
	}

	//判断topic channel 命名是否符合
	topic, channel, err := getTopicChan("REGISTER", params)
	if err != nil {
		return nil, err
	}

	//nsqd注册信息, 保存到缓存中
	if channel != "" {
		key := Registration{"channel", topic, channel}
		if p.ctx.nsqlookupd.DB.AddProducer(key, &Producer{peerInfo: client.peerInfo}) {
			p.ctx.nsqlookupd.logf(LOG_INFO, "DB: client(%s) REGISTER category:%s key:%s subkey:%s",
				client, "channel", topic, channel)
		}
	}
	key := Registration{"topic", topic, ""}
	if p.ctx.nsqlookupd.DB.AddProducer(key, &Producer{peerInfo: client.peerInfo}) {
		p.ctx.nsqlookupd.logf(LOG_INFO, "DB: client(%s) REGISTER category:%s key:%s subkey:%s",
			client, "topic", topic, "")
	}

	return []byte("OK"), nil
}

//UNREGISTER nsqd向nsqlookup注销
func (p *LookupProtocolV1) UNREGISTER(client *ClientV1, reader *bufio.Reader, params []string) ([]byte, error) {
	//未鉴权获取nsqd信息?
	if client.peerInfo == nil {
		return nil, protocol.NewFatalClientErr(nil, "E_INVALID", "client must IDENTIFY")
	}

	//判断topic channel 命名是否符合
	topic, channel, err := getTopicChan("UNREGISTER", params)
	if err != nil {
		return nil, err
	}

	//从缓存中删除nsqd和topic channel的绑定关系
	if channel != "" {
		//单单删除channel
		key := Registration{"channel", topic, channel}
		removed, left := p.ctx.nsqlookupd.DB.RemoveProducer(key, client.peerInfo.id)
		if removed {
			p.ctx.nsqlookupd.logf(LOG_INFO, "DB: client(%s) UNREGISTER category:%s key:%s subkey:%s",
				client, "channel", topic, channel)
		}
		// for ephemeral channels, remove the channel as well if it has no producers
		if left == 0 && strings.HasSuffix(channel, "#ephemeral") {
			p.ctx.nsqlookupd.DB.RemoveRegistration(key)
		}
	} else {
		// no channel was specified so this is a topic unregistration
		// remove all of the channel registrations...
		// normally this shouldn't happen which is why we print a warning message
		// if anything is actually removed
		//删除topic, 找到topic下的channel的nsqd
		registrations := p.ctx.nsqlookupd.DB.FindRegistrations("channel", topic, "*")
		for _, r := range registrations {
			removed, _ := p.ctx.nsqlookupd.DB.RemoveProducer(r, client.peerInfo.id)
			if removed {
				p.ctx.nsqlookupd.logf(LOG_WARN, "client(%s) unexpected UNREGISTER category:%s key:%s subkey:%s",
					client, "channel", topic, r.SubKey)
			}
		}

		key := Registration{"topic", topic, ""}
		removed, left := p.ctx.nsqlookupd.DB.RemoveProducer(key, client.peerInfo.id)
		if removed {
			p.ctx.nsqlookupd.logf(LOG_INFO, "DB: client(%s) UNREGISTER category:%s key:%s subkey:%s",
				client, "topic", topic, "")
		}
		if left == 0 && strings.HasSuffix(topic, "#ephemeral") {
			p.ctx.nsqlookupd.DB.RemoveRegistration(key)
		}
	}

	return []byte("OK"), nil
}

/**
1.读取head,得到body size
2.读取body
3.解析得到nsqd的信息(主机名 远程地址 tcp-port http-port)
4.本地保存nsqd信息, 更新访问时间
5.返回本nsqlookup信息给nsqd
 */
func (p *LookupProtocolV1) IDENTIFY(client *ClientV1, reader *bufio.Reader, params []string) ([]byte, error) {
	var err error

	//只能鉴权一次
	if client.peerInfo != nil {
		return nil, protocol.NewFatalClientErr(err, "E_INVALID", "cannot IDENTIFY again")
	}

	//通信协议: head4字节(body size)+body
	//读取body size
	var bodyLen int32
	err = binary.Read(reader, binary.BigEndian, &bodyLen)
	if err != nil {
		return nil, protocol.NewFatalClientErr(err, "E_BAD_BODY", "IDENTIFY failed to read body size")
	}

	//读取bodyLen个字节的body数据
	body := make([]byte, bodyLen)
	_, err = io.ReadFull(reader, body)
	if err != nil {
		return nil, protocol.NewFatalClientErr(err, "E_BAD_BODY", "IDENTIFY failed to read body")
	}

	// body is a json structure with producer information
	//反序列化得到nsqd peer的信息(主机名 远程地址 tcp-port http-port)
	peerInfo := PeerInfo{id: client.RemoteAddr().String()}
	err = json.Unmarshal(body, &peerInfo)
	if err != nil {
		return nil, protocol.NewFatalClientErr(err, "E_BAD_BODY", "IDENTIFY failed to decode JSON body")
	}

	peerInfo.RemoteAddress = client.RemoteAddr().String()

	// require all fields
	//必须包含这些信息(主机名 远程地址 tcp-port http-port)
	if peerInfo.BroadcastAddress == "" || peerInfo.TCPPort == 0 || peerInfo.HTTPPort == 0 || peerInfo.Version == "" {
		return nil, protocol.NewFatalClientErr(nil, "E_BAD_BODY", "IDENTIFY missing fields")
	}

	//更新访问时间
	atomic.StoreInt64(&peerInfo.lastUpdate, time.Now().UnixNano())

	p.ctx.nsqlookupd.logf(LOG_INFO, "CLIENT(%s): IDENTIFY Address:%s TCP:%d HTTP:%d Version:%s",
		client, peerInfo.BroadcastAddress, peerInfo.TCPPort, peerInfo.HTTPPort, peerInfo.Version)

	//保存peerInfo信息
	client.peerInfo = &peerInfo
	if p.ctx.nsqlookupd.DB.AddProducer(Registration{"client", "", ""}, &Producer{peerInfo: client.peerInfo}) {
		p.ctx.nsqlookupd.logf(LOG_INFO, "DB: client(%s) REGISTER category:%s key:%s subkey:%s", client, "client", "", "")
	}

	// build a response
	//返回nsqlookup信息
	data := make(map[string]interface{})
	data["tcp_port"] = p.ctx.nsqlookupd.RealTCPAddr().Port
	data["http_port"] = p.ctx.nsqlookupd.RealHTTPAddr().Port
	data["version"] = version.Binary
	hostname, err := os.Hostname()
	if err != nil {
		log.Fatalf("ERROR: unable to get hostname %s", err)
	}
	data["broadcast_address"] = p.ctx.nsqlookupd.opts.BroadcastAddress
	data["hostname"] = hostname

	response, err := json.Marshal(data)
	if err != nil {
		p.ctx.nsqlookupd.logf(LOG_ERROR, "marshaling %v", data)
		return []byte("OK"), nil
	}
	return response, nil
}

//PING nsqd定时发送ping来保持心跳, 维护连接健康
func (p *LookupProtocolV1) PING(client *ClientV1, params []string) ([]byte, error) {
	if client.peerInfo != nil {
		// we could get a PING before other commands on the same client connection
		cur := time.Unix(0, atomic.LoadInt64(&client.peerInfo.lastUpdate))
		now := time.Now()
		p.ctx.nsqlookupd.logf(LOG_INFO, "CLIENT(%s): pinged (last ping %s)", client.peerInfo.id,
			now.Sub(cur))
		atomic.StoreInt64(&client.peerInfo.lastUpdate, now.UnixNano())
	}
	return []byte("OK"), nil
}
