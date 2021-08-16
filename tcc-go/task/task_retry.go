package task

import (
	"encoding/json"
	"tcc_transaction/constant"
	"tcc_transaction/global/various"
	"tcc_transaction/log"
	"tcc_transaction/model"
	"tcc_transaction/store/data"
	"tcc_transaction/util"
	"time"
)

// 定时重试
func taskToRetry(needRollbackData []*data.RequestInfo) {
	log.Infof("start to retry, data is : %+v", len(needRollbackData))
	for _, v := range needRollbackData {
		if len(v.SuccessSteps) == 0 {
			continue
		}

		if v.Times >= constant.RetryTimes {
			continue
		}

		runtimeAPI, err := various.GetApiWithURL(v.Url)
		if err != nil {
			log.Errorf("get api by url of [request_info] failed, please check it. error information is: %s", err)
			continue
		}
		runtimeAPI.RequestInfo = v

		if v.Status == constant.RequestInfoStatus2 {
			go confirm(runtimeAPI)
		} else if v.Status == constant.RequestInfoStatus4 {
			go cancel(runtimeAPI)
		}
	}
}

func confirm(api *model.RuntimeApi) {
	var isErr bool
	ri := api.RequestInfo
	for _, v := range ri.SuccessSteps {
		// confirm
		cURL := util.URLRewrite(api.UrlPattern, ri.Url, api.Nodes[v.Index].Confirm.Url)
		_, err := util.HttpForward(cURL, api.Nodes[v.Index].Confirm.Method, []byte(v.Param), nil, time.Duration(api.Nodes[v.Index].Confirm.Timeout))
		if err != nil {
			isErr = true
			log.Errorf("asynchronous to confirm failed, please check it. error information is: %s", err)
			continue
		}
		various.C.UpdateSuccessStepStatus(api.RequestInfo.Id, v.Id, constant.RequestTypeConfirm)
	}
	if !isErr {
		various.C.Confirm(ri.Id)
	} else {
		various.C.UpdateRequestInfoTimes(ri.Id)
	}
}

func cancel(api *model.RuntimeApi) {
	var isErr bool
	ri := api.RequestInfo
	for _, v := range ri.SuccessSteps {
		// cancel
		cURL := util.URLRewrite(api.UrlPattern, ri.Url, api.Nodes[v.Index].Cancel.Url)
		dt, err := util.HttpForward(cURL, api.Nodes[v.Index].Cancel.Method, []byte(v.Param), nil, time.Duration(api.Nodes[v.Index].Cancel.Timeout))
		if err != nil {
			isErr = true
			log.Errorf("asynchronous to cancel failed, please check it. error information is: %s", err)
			continue
		}

		var rst *util.Response
		err = json.Unmarshal(dt, &rst)
		if err != nil {
			isErr = true
			log.Errorf("asynchronous to cancel, the content format of response back is wrong, please check it. error information is: %s", err)
			continue
		}

		if rst.Code != constant.Success {
			isErr = true
			log.Errorf("asynchronous to cancel, response back content is wrong, please check it. error information is: %s", err)
			continue
		}

		various.C.UpdateSuccessStepStatus(api.RequestInfo.Id, v.Id, constant.RequestTypeCancel)
	}
	if !isErr {
		various.C.UpdateRequestInfoStatus(constant.RequestInfoStatus3, ri.Id)
	} else {
		various.C.UpdateRequestInfoTimes(ri.Id)
	}
}
