/*
Copyright 2012 Google Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package singleflight provides a duplicate function call suppression
// mechanism.
package singleflight

import "sync"

// call is an in-flight or completed Do call
type call struct {
	wg  sync.WaitGroup
	val interface{}
	err error
}

// Group represents a class of work and forms a namespace in which
// units of work can be executed with duplicate suppression.
type Group struct {
	mu sync.Mutex       // protects m
	m  map[string]*call // lazily initialized
}

// Do executes and returns the results of the given function, making
// sure that only one execution is in-flight for a given key at a
// time. If a duplicate comes in, the duplicate caller waits for the
// original to complete and receives the same results.
// 经典算法: 并发只执行一次, 其他阻塞等待结果
func (g *Group) Do(key string, fn func() (interface{}, error)) (interface{}, error) {
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*call)
	}
	// 已经有请求, 阻塞等待
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		// 其他请求完成, 返回结果
		return c.val, c.err
	}
	//
	c := new(call)
	// add 1, 上面wait就会阻塞
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	// todo, 设置一个超时, 不然阻塞时间不可控, 造成缓存服务不可用
	c.val, c.err = fn()
	// 直至请求完成才done, 上面的wait继续往下执行
	c.wg.Done()

	g.mu.Lock()
	delete(g.m, key)
	g.mu.Unlock()

	return c.val, c.err
}
