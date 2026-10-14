const t = await fetch('http://127.0.0.1:9224/json/list').then(r=>r.json())
console.error('step1 targets', t.filter(x=>x.type==='page').length)
const page = t.find(x=>x.type==='page')
console.error('step2 ws url', !!page.webSocketDebuggerUrl)
const ws = new WebSocket(page.webSocketDebuggerUrl)
const opened = await Promise.race([
  new Promise(r=>{ws.addEventListener('open',()=>r('open'),{once:true});ws.addEventListener('error',e=>r('error:'+e.message),{once:true})}),
  new Promise(r=>setTimeout(()=>r('TIMEOUT'),8000)),
])
console.error('step3', opened)
if (opened === 'open') {
  const reply = await Promise.race([
    new Promise(res=>{ws.addEventListener('message',ev=>res(ev.data),{once:true});ws.send(JSON.stringify({id:1,method:'Page.enable'}))}),
    new Promise(r=>setTimeout(()=>r('TIMEOUT'),8000)),
  ])
  console.error('step4', String(reply).slice(0,80))
}
process.exit(0)
