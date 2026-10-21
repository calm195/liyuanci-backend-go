"""T004/REQ-018、020：真实进程健康/关闭示例入口/完成日志与优雅退出检查。"""
import json
import os
import socket
import subprocess
import time
import urllib.error
import urllib.request

with socket.socket() as sock:
 sock.bind(('127.0.0.1',0))
 port=sock.getsockname()[1]
env={**os.environ,'APP_SERVER_ADDRESS':f'127.0.0.1:{port}','APP_ENVIRONMENT':'foundation','APP_DATABASE_DRIVER':'','APP_REDIS_ENABLED':'false','APP_ROCKETMQ_ENABLED':'false'}
proc=subprocess.Popen(['./bin/server','-config',''],env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
try:
 for _ in range(50):
  try:
   urllib.request.urlopen(f'http://127.0.0.1:{port}/healthz',timeout=1)
   break
  except OSError:time.sleep(.1)
 for method,path,expected in [('GET','/healthz',200),('GET','/readyz',503),('POST','/api/v1/users',404),('GET','/api/v1/me',404)]:
  req=urllib.request.Request(f'http://127.0.0.1:{port}'+path,method=method)
  try:response=urllib.request.urlopen(req,timeout=2)
  except urllib.error.HTTPError as error:response=error
  assert response.status==expected,(method,path,response.status)
  assert response.headers.get('X-Request-ID')
  print('T004/REQ-018,020',method,path,response.status)
finally:
 proc.terminate()
 try:output=proc.communicate(timeout=15)[0]
 except subprocess.TimeoutExpired:
  proc.kill()
  output=proc.communicate()[0]
 logs=[json.loads(line) for line in output.splitlines() if line.startswith('{')]
 assert sum(line.get('msg')=='HTTP请求处理完成' for line in logs)>=4
 assert any(line.get('msg')=='服务关闭完成' for line in logs)
 print('T004 request correlation and graceful shutdown PASS')
