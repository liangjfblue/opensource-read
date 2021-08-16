package main

import (
	"fmt"
	"net/http"
	"tcc_transaction/constant"
	"tcc_transaction/global/various"
	"tcc_transaction/log"
	"tcc_transaction/model"
	"tcc_transaction/store/data"
	"tcc_transaction/util"
)

type proxy struct {
	t tcc
}

// 处理客户端请求
func (p *proxy) process(writer http.ResponseWriter, request *http.Request) {
	var response = &util.Response{}
	params := util.GetParams(request)
	log.Infof("welcome to tcc. url is %s, and param is %s", request.RequestURI, string(params))

	// 将请求信息持久化 幂等性, 防止宕机丢信息
	ri := &data.RequestInfo{
		Url:    request.RequestURI[len(serverName)+1:],
		Method: request.Method,
		Param:  string(params),
	}
	err := various.C.InsertRequestInfo(ri)
	if err != nil {
		response.Code = constant.InsertTccDataErrCode
		response.Msg = err.Error()
		util.ResponseWithJson(writer, response)
		log.Errorf("program have a bug, please check it. error info: %s", err)
		return
	}

	// 客户端对tcc请求转换为真正服务的try/confirm/cancel接口
	runtimeAPI, err := various.GetApiWithURL(request.RequestURI[len(serverName)+1:])
	if err != nil {
		response.Code = constant.NotFoundErrCode
		response.Msg = err.Error()
		util.ResponseWithJson(writer, response)
		log.Warnf("there is no request info in configuration")
		return
	}
	runtimeAPI.RequestInfo = ri

	// 转发--Try
	cancelSteps, err := p.try(request, runtimeAPI)

	if err != nil { // 回滚
		if len(cancelSteps) > 0 {
			go p.cancel(request, runtimeAPI, cancelSteps)
		}
		log.Errorf("try failed, error info is: %s", err)
		response.Code = constant.InsertTccDataErrCode
		response.Msg = err.Error()
		util.ResponseWithJson(writer, response)
		return
	} else { // 提交
		go p.confirm(request, runtimeAPI)
	}
	response.Code = constant.Success
	util.ResponseWithJson(writer, response)
	return
}

func (p *proxy) try(r *http.Request, api *model.RuntimeApi) ([]*model.RuntimeTCC, error) {
	var nextCancelStep []*model.RuntimeTCC

	// 没转发配置api
	tryNodes := api.Nodes
	if len(tryNodes) == 0 {
		return nextCancelStep, fmt.Errorf("no method need to execute")
	}

	// 转发try
	success, err := p.t.Try(r, api)

	// 插入tcc操作成功表
	err2 := various.C.BatchInsertSuccessStep(success)
	if err != nil || err2 != nil {
		// 失败的话, 找出分布式事务哪个步骤失败了.
		// 比如: 订单校验->扣费->生成订单, 若扣费失败, 返回扣费步骤的操作节点, 后续会执行所有的cancel
		for _, node := range api.Nodes {
			for _, s := range success {
				if node.Index == s.Index {
					node.SuccessStep = s
					nextCancelStep = append(nextCancelStep, node)
				}
			}
		}
		return nextCancelStep, err
	}
	if err2 != nil {
		return nextCancelStep, err2
	}
	if len(success) == 0 {
		return nil, fmt.Errorf("no successful method of execution")
	}
	return nextCancelStep, nil
}

func (p *proxy) confirm(r *http.Request, api *model.RuntimeApi) error {
	err := p.t.Confirm(r, api)
	if err != nil {
		various.C.UpdateRequestInfoStatus(constant.RequestInfoStatus2, api.RequestInfo.Id)
		return err
	}
	// 处理成功后，修改状态
	various.C.Confirm(api.RequestInfo.Id)
	// 全部提交成功，则修改状态为提交成功，避免重复调用
	various.C.UpdateRequestInfoStatus(constant.RequestInfoStatus1, api.RequestInfo.Id)
	return nil
}

func (p *proxy) cancel(r *http.Request, api *model.RuntimeApi, nodes []*model.RuntimeTCC) error {
	ids, err := p.t.Cancel(r, api, nodes)
	if err != nil {
		// 回滚失败
		various.C.UpdateRequestInfoStatus(constant.RequestInfoStatus4, api.RequestInfo.Id)
		return err
	}
	for _, id := range ids {
		// 执行了cancel
		various.C.UpdateSuccessStepStatus(api.RequestInfo.Id, id, constant.RequestTypeCancel)
	}
	// 回滚成功
	various.C.UpdateRequestInfoStatus(constant.RequestInfoStatus3, api.RequestInfo.Id)
	return nil
}
