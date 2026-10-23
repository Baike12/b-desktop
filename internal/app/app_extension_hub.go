package app

// extensionStore 按 key 惰性创建并复用扩展组件（skills、memory、hooks、mcp、subagent、
// task、telemetry 等）。init 只会被调用一次；返回错误时不缓存，下一次调用会重试。
//
// init 在 extMu 持有期间执行，因此它不得回调 extensionStore（会死锁），
// 也不得调用依赖 a.mu 的方法。组件构造应当只做本地 IO，网络连接放入组件自身的惰性路径。
func (a *App) extensionStore(key string, init func() (any, error)) (any, error) {
	a.extMu.Lock()
	defer a.extMu.Unlock()
	if value, ok := a.extData[key]; ok {
		return value, nil
	}
	value, err := init()
	if err != nil {
		return nil, err
	}
	if a.extData == nil {
		a.extData = make(map[string]any)
	}
	a.extData[key] = value
	return value, nil
}
