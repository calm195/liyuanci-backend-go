"""T004/REQ-018、020：真实容器入口检查；基础连通不等于业务ready或认证完成。"""
import urllib.error
import urllib.request

for method,path,expected in [('GET','/healthz',200),('GET','/readyz',503),('POST','/api/v1/users',404),('GET','/api/v1/me',404)]:
 request=urllib.request.Request('http://127.0.0.1:18080'+path,method=method)
 try:response=urllib.request.urlopen(request,timeout=5)
 except urllib.error.HTTPError as error:response=error
 assert response.status==expected,(method,path,response.status)
 assert response.headers.get('X-Request-ID'),path
 print('PASS T004/REQ-018,020 container API',method,path,response.status)
