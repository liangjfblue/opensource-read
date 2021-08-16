package various

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"regexp"
	"tcc_transaction/global/config"
	"tcc_transaction/log"
	"tcc_transaction/model"
	"tcc_transaction/store/config/etcd"
	"tcc_transaction/store/data"
	"tcc_transaction/store/data/leveldb"
	"tcc_transaction/store/data/mysql"
	"time"
)

// 思路:
// api配置存放在etcd, 并支持监听etcd的api配置变更, 更新本地配置
// TCC事务状态表默认存储组件是leveldb
// 业务请求先请求TCC服务, 根据url从api配置中匹配对应的try/confirm/cancel接口, 再去请求

var (
	// C 数据库连接
	C        data.DataClient
	// TCC参与服务的api配置
	apis     []*model.Api
	EtcdC, _ = etcd3.NewEtcd3Client([]string{"localhost:2379"}, int(time.Minute), "", "", nil)
)

func InitAll() {
	flag.Parse()
	var err error
	// TCC事务记录表存储组件
	C, err = mysql.NewMysqlClient(*config.MysqlUsername, *config.MysqlPassword, *config.MysqlHost, *config.MysqlPort, *config.MysqlDatabase)
	if err != nil {
		C, err = leveldb.NewLevelDB(*config.DBPath)
		if err != nil {
			panic(err)
		}
	}
	log.InitLogrus(*config.LogFilePath, *config.LogLevel)

	// 从etcd加载api配置
	LoadApiFromEtcd()
	// 监听etcd
	WatchApi()
}

// GetApiWithURL 根据请求TCC服务的url匹配为真正服务的url, 去请求真正的try/confirm/cancel接口
func GetApiWithURL(url string) (*model.RuntimeApi, error) {
	for _, v := range apis {
		reg, _ := regexp.Compile(v.UrlPattern)
		if reg.MatchString(url) {
			ra := &model.RuntimeApi{
				UrlPattern: v.UrlPattern,
				Nodes:      model.ConverToRuntime(v.Nodes), // 操作
			}
			return ra, nil
		}
	}
	return nil, fmt.Errorf("there is no api to run")
}

// LoadApiFromEtcd 从etcd中加载api配置
func LoadApiFromEtcd() {
	var parseToApi = func(param [][]byte) []*model.Api {
		var rsts = make([]*model.Api, 0, len(param))
		for _, bs := range param {
			var api *model.Api
			err := json.Unmarshal(bs, &api)
			if err != nil {
				panic(err)
			}
			rsts = append(rsts, api)
		}
		return rsts
	}

	// 获取
	data, err := EtcdC.List(context.Background(), *config.ApiKeyPrefix)
	if err != nil {
		panic(err)
	}
	apis = parseToApi(data)
}

// WatchApi 监控TCC API资源的变更, 此处是监听etcd, api配置都在etcd
func WatchApi() {
	del := func(idx int) {
		apis = append(apis[:idx], apis[idx+1:]...)
	}

	// 新增api
	add := func(v []byte) error {
		var api *model.Api
		err := json.Unmarshal(v, &api)
		if err != nil {
			return err
		}
		apis = append(apis, api)
		return nil
	}

	// 更新
	modify := func(idx int, v []byte) error {
		del(idx)
		return add(v)
	}

	// 从api列表中获取api的下标
	getIdx := func(k []byte) int {
		for idx, v := range apis {
			if v.UrlPattern == string(k)[len(*config.ApiKeyPrefix):] {
				return idx
			}
		}
		return 0
	}

	// etcd 监听事件回调
	callback := func(k, v []byte, tpe string) {
		switch tpe {
		case etcd3.WatchEventTypeD:
			del(getIdx(k))
		case etcd3.WatchEventTypeC:
			add(v)
		case etcd3.WatchEventTypeM:
			modify(getIdx(k), v)
		}
	}

	// 监听etcd
	EtcdC.WatchTree(context.Background(), *config.ApiKeyPrefix, callback)
}
