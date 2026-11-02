// Generates our own tiny playable MP4 fixture; no external/user media is used.
import { chromium } from '@playwright/test'
import { mkdir, writeFile } from 'node:fs/promises'
const browser = await chromium.launch()
try {
  const page = await browser.newPage()
  const bytes = await page.evaluate(async () => {
    if (!MediaRecorder.isTypeSupported('video/mp4')) throw new Error('MP4 recording is unavailable')
    const canvas = document.createElement('canvas'); canvas.width = 160; canvas.height = 90
    const context = canvas.getContext('2d')
    const stream = canvas.captureStream(10), recorder = new MediaRecorder(stream, { mimeType: 'video/mp4' })
    const chunks = []
    let frame = 0
    const draw = () => { context.fillStyle = '#eef3f1'; context.fillRect(0, 0, 160, 90); context.fillStyle = '#eb6946'; context.fillRect((frame++ * 5) % 130, 30, 30, 30); context.fillStyle = '#19222b'; context.font = '12px sans-serif'; context.fillText('GoStream test', 10, 17) }
    draw()
    const timer = setInterval(draw, 100)
    return await new Promise((resolve, reject) => {
      recorder.ondataavailable = event => chunks.push(event.data)
      recorder.onerror = reject
      recorder.onstop = async () => { clearInterval(timer); stream.getTracks().forEach(track => track.stop()); resolve([...new Uint8Array(await new Blob(chunks, { type: 'video/mp4' }).arrayBuffer())]) }
      recorder.start(); setTimeout(() => recorder.stop(), 1500)
    })
  })
  const directory = new URL('../tests/fixtures/', import.meta.url)
  await mkdir(directory, { recursive: true })
  await writeFile(new URL('playable.mp4', directory), Buffer.from(bytes))
  process.stdout.write(`Created playable MP4 fixture (${bytes.length} bytes)\n`)
} finally { await browser.close() }
