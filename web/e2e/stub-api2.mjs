import http from 'node:http'
http.createServer((req,res)=>{
  res.setHeader('X-Stub','yes')
  if(req.url==='/api/healthz'){ res.writeHead(200,{'Content-Type':'application/json'}); res.end(JSON.stringify({status:'ok',db:true})); return }
  res.writeHead(404); res.end('stub-404')
}).listen(8099,'127.0.0.1',()=>console.log('stub on 8099'))
