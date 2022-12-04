# RPC

远程过程调用

client - stub - socket - network - socket - stub - server

客户端发起调用，stub封包，socket传至网络

服务器socket收到请求，stub拆包，执行被调用函数

服务器调用完毕，stub封包，socket传至网络

客户端socket收到响应，stub拆包，读出结果继续其它工作
