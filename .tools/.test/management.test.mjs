import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { mkdtemp, rm } from 'node:fs/promises'
import { createServer } from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { setTimeout as delay } from 'node:timers/promises'
import { pathToFileURL } from 'node:url'

/** 로컬 테스트 서버를 임의 포트로 연다. */
async function listen(server) {
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  return `http://127.0.0.1:${server.address().port}`
}

/** 지정한 시작 로그가 나온 테스트 프로세스만 사용하고 종료를 등록한다. */
async function start(t, exe, args, marker, env = {}) {
  assert.ok(exe, 'KVC_TEST_KAFKA와 KVC_TEST_CONSOLE에 테스트 실행 파일을 지정해야 합니다')
  const child = spawn(exe, args, { env: { ...process.env, ...env } })
  const closed = once(child, 'close').catch(() => {})
  t.after(async () => { child.kill(); await closed })
  let log = ''
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(log || `시작 실패: ${exe}`)), 12_000)
    child.once('error', reject)
    child.once('close', () => reject(new Error(log || `프로세스 종료: ${exe}`)))
    child.stderr.on('data', (chunk) => {
      log = (log + chunk).slice(-3000)
      if (log.includes(marker)) { clearTimeout(timer); resolve() }
    })
    closed.then(() => clearTimeout(timer))
  })
}

/** 서버가 시작 로그를 기록한 뒤 실제 HTTP 요청을 받을 때까지 기다린다. */
async function waitForHTTP(root) {
  for (let attempt = 0; ; attempt++) {
    try {
      assert.equal((await fetch(`${root}/api/settings`)).status, 200)
      return
    } catch (err) {
      if (attempt >= 40 || err.cause?.code !== 'ECONNREFUSED') throw err
      await delay(50)
    }
  }
}

/** Connect REST의 등록·재시작·삭제 계약과 오류 응답을 재현한다. */
function fakeConnect(calls, connectors) {
  return createServer(async (req, res) => {
    const url = new URL(req.url, 'http://localhost')
    if (req.method === 'GET') {
      if (url.pathname === '/prefix/connectors') {
        res.end(JSON.stringify(Object.fromEntries([...connectors].map(([name, config]) => [
          name, { info: { name, config, type: 'source' }, status: { connector: { state: 'RUNNING' } } },
        ]))))
      } else res.end(JSON.stringify({ version: '3.9.0' }))
      return
    }
    let text = ''
    for await (const chunk of req) text += chunk
    const body = text ? JSON.parse(text) : null
    calls.push({ method: req.method, path: req.url, body })
    if (req.method === 'POST' && url.pathname === '/prefix/connectors') {
      if (body.name === 'redirect') {
        res.writeHead(307, { Location: '/prefix/redirected' }).end()
      } else if (connectors.has(body.name)) {
        res.writeHead(409).end('절대 응답으로 노출되지 않아야 할 비밀')
      } else {
        connectors.set(body.name, body.config)
        res.writeHead(201).end(JSON.stringify(body))
      }
      return
    }
    if (req.method === 'DELETE' && url.pathname.startsWith('/prefix/connectors/')) {
      const name = decodeURIComponent(url.pathname.slice('/prefix/connectors/'.length))
      res.writeHead(connectors.delete(name) ? 204 : 404).end()
      return
    }
    res.writeHead(url.pathname.endsWith('/restart') ? 202 : 404).end()
  })
}

/** 선택적으로 Chromium에서 각 관리 버튼과 입력 방식을 끝까지 실행한다. */
async function checkGUI(t, root, calls) {
  const { chromium } = await import(pathToFileURL(process.env.KVC_TEST_PLAYWRIGHT).href)
  const browser = await chromium.launch({ channel: 'msedge' })
  t.after(() => browser.close())
  const page = await browser.newPage({ locale: 'ko-KR' })
  const pageErrors = []
  page.on('pageerror', (err) => pageErrors.push(err.message))
  page.setDefaultTimeout(10_000)
  await page.goto(`${root}/#/topics/orders`)
  await page.getByRole('button', { name: '설정', exact: true }).click()
  const row = page.getByRole('row').filter({ has: page.getByRole('cell', { name: 'retention.ms', exact: true }) })
  await row.getByRole('button', { name: '변경: retention.ms' }).click()
  await page.getByRole('textbox', { name: '값' }).fill('172800000')
  await page.getByRole('button', { name: '저장', exact: true }).click()
  await row.getByRole('cell', { name: '172800000', exact: true }).waitFor()
  await row.getByRole('button', { name: '오버라이드 삭제: retention.ms' }).click()
  await page.getByRole('button', { name: '취소', exact: true }).click()
  await row.getByRole('cell', { name: '172800000', exact: true }).waitFor()
  await row.getByRole('button', { name: '오버라이드 삭제: retention.ms' }).click()
  await page.getByRole('button', { name: '오버라이드 삭제', exact: true }).click()
  await row.getByRole('cell', { name: 'DEFAULT_CONFIG', exact: true }).waitFor()

  await page.goto(`${root}/#/connect`)
  await page.getByRole('button', { name: '커넥터 등록', exact: true }).click()
  const register = page.locator('form').getByRole('button', { name: '커넥터 등록', exact: true })
  await page.getByRole('textbox', { name: '커넥터 설정 (JSON)' }).fill('{"name":"typed","connector.class":"TestSource"}')
  await register.click()
  await page.getByRole('button', { name: '재시작: typed', exact: true }).waitFor()
  await page.getByRole('button', { name: '커넥터 등록', exact: true }).click()
  await page.getByLabel('JSON 파일 선택').setInputFiles({
    name: 'job.json', mimeType: 'application/json',
    buffer: Buffer.from('{"name":"uploaded","config":{"connector.class":"TestSource"}}'),
  })
  await register.click()
  await page.getByRole('button', { name: '재시작: uploaded', exact: true }).click()
  await page.getByRole('button', { name: '재시작', exact: true }).click()
  await page.getByRole('status').filter({ hasText: 'uploaded 재시작 요청이 접수되었습니다.' }).waitFor()
  const restartPath = '/prefix/connectors/uploaded/restart?includeTasks=true&onlyFailed=false'
  assert.ok(calls.some((call) => call.path === restartPath))
  await page.getByRole('button', { name: '삭제: uploaded', exact: true }).click()
  await page.getByRole('button', { name: '취소', exact: true }).click()
  assert.ok(!calls.some((call) => call.method === 'DELETE' && call.path.endsWith('/uploaded')))
  await page.getByRole('button', { name: '삭제: uploaded', exact: true }).click()
  await page.getByRole('button', { name: '삭제', exact: true }).click()
  await page.getByRole('button', { name: '삭제: uploaded', exact: true }).waitFor({ state: 'detached' })
  assert.deepEqual(pageErrors, [])
}

/** 내장 HTTP 서버에 접근하여 토픽과 Connect 관리 동작을 함께 검증한다. */
test('토픽 오버라이드 변경·삭제와 Kafka Connect 커넥터 수명주기', { timeout: 90_000 }, async (t) => {
  const dir = await mkdtemp(join(tmpdir(), 'kvc-management-test-'))
  t.after(() => rm(dir, { recursive: true, force: true }))
  const calls = []
  const connectors = new Map()
  const upstream = fakeConnect(calls, connectors)
  const endpoint = await listen(upstream) + '/prefix'
  t.after(() => upstream.close())
  const portPicker = createServer()
  const root = await listen(portPicker)
  await new Promise((resolve) => portPicker.close(resolve))
  await start(t, process.env.KVC_TEST_KAFKA, [], 'kfake up:')
  const brokers = '127.0.0.1:19092'
  await start(t, process.env.KVC_TEST_CONSOLE, ['-listen', new URL(root).host, '-brokers', brokers, '-connect', endpoint], 'http://' + new URL(root).host, { KVC_CONFIG: join(dir, 'profile.json') })
  await waitForHTTP(root)

  /** HTTP 관리 호출을 실행하고 기대한 결과 코드와 오류 본문을 검증한다. */
  async function request(path, status, method = 'GET', { target, body, site, custom = true } = {}) {
    const headers = { 'Content-Type': 'application/json' }
    if (custom && method !== 'GET') headers['X-KVC-Action'] = 'manage'
    if (target) headers['X-KVC-Target'] = target
    if (site) headers['Sec-Fetch-Site'] = site
    const res = await fetch(root + path, { method, body: body === undefined ? undefined : JSON.stringify(body), headers })
    const text = await res.text()
    assert.equal(res.status, status, `${method} ${path}: ${text}`)
    return { target: res.headers.get('X-KVC-Target'), text, data: text ? JSON.parse(text) : null }
  }

  const topic = '/api/topics/orders/config'
  const initial = await request(topic, 200)
  assert.ok(initial.target)
  /** 관심 있는 설정 키의 유효값과 출처를 추출한다. */
  const getConfig = (response, key) => response.data.find((c) => c.key === key)
  const retention = getConfig(initial, 'retention.ms')
  const cleanup = getConfig(initial, 'cleanup.policy')
  assert.ok(retention && cleanup)
  const alter = { key: 'retention.ms', op: 'set', value: '86400000' }
  await request(topic, 403, 'PATCH', { target: initial.target, body: alter, custom: false })
  await request(topic, 400, 'PATCH', { target: initial.target, body: { key: 'retention.ms', value: null } })
  await request(topic, 400, 'PATCH', { target: initial.target, body: { ...alter, value: 1 } })
  await request('/api/topics/nonexistent/config', 502, 'PATCH', { target: initial.target, body: alter })
  await request(topic, 204, 'PATCH', { target: initial.target, body: alter })
  const changed = await request(topic, 200)
  assert.equal(getConfig(changed, 'retention.ms').value, alter.value)
  assert.equal(getConfig(changed, 'retention.ms').source, 'DYNAMIC_TOPIC_CONFIG')
  assert.deepEqual(getConfig(changed, 'cleanup.policy'), cleanup)
  await request(topic, 204, 'PATCH', { target: changed.target, body: { key: alter.key, op: 'delete' } })
  assert.deepEqual(getConfig(await request(topic, 200), alter.key), retention)

  const path = '/api/connect/connectors'
  const list = await request('/api/connect', 200)
  assert.equal(list.data.error, undefined)
  assert.ok(list.target)
  const name = 'my job#1'
  const job = { name, config: { 'connector.class': 'TestSource', 'tasks.max': 2, 'db.password': 'my-secret' } }
  await request(path, 403, 'POST', { target: list.target, site: 'cross-site', body: job })
  await request(path, 400, 'POST', { target: list.target, body: { name, config: { name: 'wrong' } } })
  assert.equal(calls.length, 0)
  await request(path, 204, 'POST', { target: list.target, body: job })
  assert.deepEqual(calls[0], { method: 'POST', path: '/prefix/connectors', body: { name, config: { ...job.config, 'tasks.max': '2' } } })
  const listed = await request('/api/connect', 200)
  assert.equal(listed.data.connectors[0].name, name)
  assert.doesNotMatch(listed.text, /my-secret/)
  const duplicate = await request(path, 409, 'POST', { target: list.target, body: job })
  assert.doesNotMatch(duplicate.text, /절대 응답으로 노출되지 않아야 할 비밀/)
  const flat = { name: 'flat', 'connector.class': 'TestSource', enabled: true }
  await request(path, 204, 'POST', { target: list.target, body: flat })
  assert.equal(connectors.get('flat').enabled, 'true')
  await request(path, 502, 'POST', { target: list.target, body: { ...flat, name: 'redirect' } })
  assert.equal(calls.at(-1).path, '/prefix/connectors')
  await request(`${path}/${encodeURIComponent(name)}/restart`, 204, 'POST', { target: list.target })
  assert.equal(calls.at(-1).path, '/prefix/connectors/my%20job%231/restart?includeTasks=true&onlyFailed=false')
  await request(`${path}/${encodeURIComponent(name)}`, 204, 'DELETE', { target: list.target })
  assert.ok(!(await request('/api/connect', 200)).data.connectors.some((c) => c.name === name))
  await request(`${path}/${encodeURIComponent(name)}`, 404, 'DELETE', { target: list.target })

  await request('/api/settings', 200, 'PUT', { body: { brokers, connectUrl: endpoint } })
  const lastCalls = calls.length
  await request(`${path}/flat`, 409, 'DELETE', { target: list.target })
  await request(topic, 409, 'PATCH', { target: initial.target, body: alter })
  assert.equal(calls.length, lastCalls)
  if (process.env.KVC_TEST_PLAYWRIGHT) await checkGUI(t, root, calls)
})
