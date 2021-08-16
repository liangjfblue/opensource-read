package task

import (
	"context"
	"tcc_transaction/constant"
	"tcc_transaction/global/config"
	"tcc_transaction/global/various"
	"tcc_transaction/log"
	"tcc_transaction/store/data"
	"tcc_transaction/store/lock"
	"time"
)

type Task struct {
	Interval time.Duration // unit: second
	Off      chan bool     // 控制停止任务
	off      bool          // 标记当前任务是否已经停止
	F        func()
}

func (ts *Task) Start() {
	if ts.off {
		ts.off = false
		go ts.exec()
	}
}

func (ts *Task) Stop() {
	ts.off = true
	ts.Off <- true
}

func (ts *Task) exec() {
	t := time.NewTicker(time.Second * ts.Interval)
FOR:
	for {
		select {
		case <-t.C:
			go ts.F()
		case off := <-ts.Off:
			if off {
				break FOR
			}
		}
	}
}

var defaultTask = &Task{
	Interval: time.Duration(*config.TimerInterval),
	Off:      make(chan bool, 1),
	F:        retryAndSend,
}

func Start() {
	defaultTask.Start()
}

func Stop() {
	defaultTask.Stop()
}

// 重试事务, 超出重试次数, 发送邮件, 人工干预
func retryAndSend() {
	// 获取锁, 执行任务
	ctx := context.Background()
	l, err := lock.NewEtcdLock(ctx, *config.TimerInterval, constant.LockEtcdPrefix)
	if err != nil {
		log.Warnf("cannot execute task because of the lock is got failed, error info is: %s", err)
		return
	}

	err = l.Lock(ctx)
	if err != nil {
		log.Warnf("cannot execute task because of the locker is not locked, error info is: %s", err)
		return
	}
	defer l.Unlock(ctx)

	// 获取异常状态记录
	data := getBaseData()
	if len(data) == 0 {
		return
	}
	// 重试
	go taskToRetry(data)
	// 发告警邮件
	go taskToSend(data, "there are some exceptional data, please fix it soon")
}

func getBaseData() []*data.RequestInfo {
	needRollbackData, err := various.C.ListExceptionalRequestInfo()
	if err != nil {
		log.Errorf("the data that required for the task is failed to load, please check it. error information: %s", err)
		return nil
	}
	return needRollbackData
}
