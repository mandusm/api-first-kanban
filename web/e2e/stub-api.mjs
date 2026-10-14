import http from 'node:http'
http.createServer((req,res)=>{
  if(req.url==='/api/healthz'){
    res.writeHead(200,{'Content-Type':'application/json'})
    res.end(JSON.stringify({status:'ok',db:true}))
    return
  }
  res.writeHead(404); res.end('nope')
}).listen(8080,'127.0.0.1',()=>console.log('stub on 8080'))
