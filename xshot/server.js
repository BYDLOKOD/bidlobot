import express from 'express';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import puppeteer from 'puppeteer';
import { pipeline } from 'stream/promises';
import { Readable } from 'stream';
import { readFileSync } from 'fs';

const __dirname = dirname(fileURLToPath(import.meta.url));
const app = express();
const PORT = process.env.PORT || 3210;

const WATERMARK_ENABLED = process.env.WATERMARK_ENABLED === 'true';
const WATERMARK_NAME = process.env.WATERMARK_NAME || '';
const WATERMARK_HANDLE = process.env.WATERMARK_HANDLE || '';
const WATERMARK_LOGO = process.env.WATERMARK_LOGO || join(__dirname, 'assets/logo.png');
const WATERMARK_QR = process.env.WATERMARK_QR || join(__dirname, 'assets/qr.png');

let logoB64 = '';
let qrB64 = '';
try { logoB64 = 'data:image/png;base64,' + readFileSync(WATERMARK_LOGO, 'base64'); } catch {}
try { qrB64 = 'data:image/png;base64,' + readFileSync(WATERMARK_QR, 'base64'); } catch {}

const ALLOWED_VIDEO_HOSTS = new Set([
  'video.twimg.com',
  'pbs.twimg.com',
]);

let browser = null;

async function getBrowser() {
  if (!browser || !browser.connected) {
    browser = await puppeteer.launch({
      headless: true,
      executablePath: process.env.PUPPETEER_EXECUTABLE_PATH || undefined,
      args: ['--no-sandbox', '--disable-setuid-sandbox', '--disable-dev-shm-usage'],
    });
  }
  return browser;
}

function parseTweetUrl(url) {
  const match = url.match(/(?:twitter\.com|x\.com)\/(?:(\w+)\/status|i\/web\/status)\/(\d+)/);
  if (!match) return null;
  return { username: match[1] || 'i', tweetId: match[2] };
}

function formatNumber(n) {
  if (n == null) return '0';
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1).replace(/\.0$/, '') + 'M';
  if (n >= 1_000) return (n / 1_000).toFixed(1).replace(/\.0$/, '') + 'K';
  return String(n);
}

function relativeTime(timestamp) {
  const diff = Math.floor((Date.now() - timestamp * 1000) / 1000);
  if (diff < 60) return `${diff}s`;
  if (diff < 3600) return `${Math.floor(diff / 60)}m`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h`;
  const date = new Date(timestamp * 1000);
  const months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
  const now = new Date();
  if (date.getFullYear() === now.getFullYear()) {
    return `${months[date.getMonth()]} ${date.getDate()}`;
  }
  return `${months[date.getMonth()]} ${date.getDate()}, ${date.getFullYear()}`;
}

async function fetchAsBase64(url) {
  try {
    const res = await fetch(url, { headers: { 'User-Agent': 'xshot/1.0' } });
    if (!res.ok) return null;
    const buf = Buffer.from(await res.arrayBuffer());
    const ct = res.headers.get('content-type') || 'image/jpeg';
    return `data:${ct};base64,${buf.toString('base64')}`;
  } catch {
    return null;
  }
}

async function embedImages(tweet) {
  const tasks = [];
  if (tweet.author?.avatar_url) {
    tasks.push(fetchAsBase64(tweet.author.avatar_url).then(b => { if (b) tweet.author.avatar_url = b; }));
  }
  for (const photo of tweet.media?.photos || []) {
    tasks.push(fetchAsBase64(photo.url).then(b => { if (b) photo.url = b; }));
  }
  for (const video of tweet.media?.videos || []) {
    if (video.thumbnail_url) {
      tasks.push(fetchAsBase64(video.thumbnail_url).then(b => { if (b) video.thumbnail_url = b; }));
    }
  }
  await Promise.all(tasks);
}

function escapeHtml(str) {
  if (!str) return '';
  return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

function buildMediaHtml(tweet) {
  const photos = tweet.media?.photos || [];
  const videos = tweet.media?.videos || [];

  if (photos.length === 0 && videos.length === 0) return '';

  let html = '';

  if (videos.length > 0) {
    const thumb = videos[0].thumbnail_url || '';
    html += `<div class="media-grid count-1">
      <div class="video-thumb">
        <img src="${escapeHtml(thumb)}" crossorigin="anonymous" alt="" />
        <div class="play-badge">▶</div>
      </div>
    </div>`;
  } else if (photos.length > 0) {
    html += `<div class="media-grid count-${Math.min(photos.length, 4)}">`;
    for (const photo of photos.slice(0, 4)) {
      html += `<img src="${escapeHtml(photo.url)}" crossorigin="anonymous" alt="" />`;
    }
    html += '</div>';
  }

  return html;
}

function buildTweetHtml(tweet, { showWatermark = WATERMARK_ENABLED } = {}) {
  const time = relativeTime(tweet.created_timestamp);
  const textHtml = escapeHtml(tweet.text).replace(/\n/g, '<br>');
  const media = buildMediaHtml(tweet);

  return `<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body {
    background: transparent;
    display: inline-block;
  }
  .card {
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
    background: #fff;
    border: 1px solid #e1e8ed;
    border-radius: 16px;
    padding: 16px;
    width: 550px;
    color: #0f1419;
  }
  .header {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-bottom: 12px;
  }
  .avatar {
    width: 48px;
    height: 48px;
    border-radius: 50%;
    object-fit: cover;
    flex-shrink: 0;
  }
  .author-info {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .author-top {
    display: flex;
    align-items: center;
    gap: 4px;
    font-size: 15px;
    font-weight: 700;
    line-height: 1.2;
  }
  .author-name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .verified {
    width: 18px;
    height: 18px;
    flex-shrink: 0;
  }
  .handle-line {
    font-size: 15px;
    color: #536471;
    line-height: 1.2;
  }
  .logo {
    margin-left: auto;
    flex-shrink: 0;
  }
  .logo svg { width: 24px; height: 24px; }
  .text {
    font-size: 17px;
    line-height: 1.45;
    margin-bottom: 12px;
    word-wrap: break-word;
    white-space: pre-wrap;
  }
  .media-grid {
    display: grid;
    gap: 2px;
    border-radius: 16px;
    overflow: hidden;
    margin-bottom: 12px;
  }
  .media-grid img {
    width: 100%;
    height: 100%;
    object-fit: cover;
    display: block;
  }
  .media-grid.count-1 { grid-template-columns: 1fr; }
  .media-grid.count-1 img, .media-grid.count-1 .video-thumb { max-height: 500px; }
  .media-grid.count-2 {
    grid-template-columns: 1fr 1fr;
    max-height: 290px;
  }
  .media-grid.count-3 {
    grid-template-columns: 1fr 1fr;
    grid-template-rows: 1fr 1fr;
    max-height: 290px;
  }
  .media-grid.count-3 img:first-child { grid-row: 1 / 3; }
  .media-grid.count-4 {
    grid-template-columns: 1fr 1fr;
    grid-template-rows: 1fr 1fr;
    max-height: 290px;
  }
  .video-thumb {
    position: relative;
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .video-thumb img {
    max-height: 500px;
  }
  .play-badge {
    position: absolute;
    background: rgba(0,0,0,0.6);
    color: #fff;
    width: 56px;
    height: 56px;
    border-radius: 50%;
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 24px;
  }
  .stats {
    display: flex;
    gap: 24px;
    padding-top: 12px;
    border-top: 1px solid #e1e8ed;
  }
  .stat {
    display: flex;
    align-items: center;
    gap: 6px;
    color: #536471;
    font-size: 14px;
  }
  .stat svg { width: 18px; height: 18px; fill: #536471; }
  .stat.likes svg { fill: #f91880; }
  .stat.likes { color: #f91880; }
  .watermark {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 12px 0 0;
    margin-top: 12px;
    border-top: 1px solid #e1e8ed;
  }
  .wm-left {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .wm-logo {
    width: 36px;
    height: 36px;
    border-radius: 50%;
    object-fit: cover;
  }
  .wm-text {
    display: flex;
    flex-direction: column;
  }
  .wm-name {
    font-size: 14px;
    font-weight: 700;
    color: #0f1419;
    line-height: 1.2;
  }
  .wm-handle {
    font-size: 13px;
    color: #536471;
    line-height: 1.2;
  }
  .wm-qr {
    width: 56px;
    height: 56px;
    border-radius: 6px;
  }
</style>
</head>
<body>
<div class="card" id="card">
  <div class="header">
    <img class="avatar" src="${escapeHtml(tweet.author?.avatar_url || '')}" crossorigin="anonymous" alt="" />
    <div class="author-info">
      <div class="author-top">
        <span class="author-name">${escapeHtml(tweet.author?.name)}</span>
        ${tweet.author?.verified ? '<svg class="verified" viewBox="0 0 22 22"><path fill="#1d9bf0" d="M20.396 11c-.018-.646-.215-1.275-.57-1.816-.354-.54-.852-.972-1.438-1.246.223-.607.27-1.264.14-1.897-.131-.634-.437-1.218-.882-1.687-.47-.445-1.053-.75-1.687-.882-.633-.13-1.29-.083-1.897.14-.273-.587-.704-1.086-1.245-1.44S11.647 1.62 11 1.604c-.646.017-1.273.213-1.813.568s-.969.855-1.24 1.44c-.608-.223-1.267-.272-1.902-.14-.635.13-1.22.436-1.69.882-.445.47-.749 1.055-.878 1.69-.13.633-.08 1.29.144 1.896-.587.274-1.087.705-1.443 1.245-.356.54-.555 1.17-.574 1.817.02.647.218 1.276.574 1.817.356.54.856.972 1.443 1.245-.224.606-.274 1.263-.144 1.896.13.636.433 1.221.878 1.69.47.446 1.055.752 1.69.883.635.13 1.294.083 1.902-.143.271.586.702 1.084 1.24 1.438.54.354 1.167.551 1.813.568.647-.016 1.276-.213 1.817-.567s.972-.854 1.245-1.44c.604.225 1.261.276 1.894.146.633-.13 1.218-.434 1.69-.88.445-.47.749-1.055.88-1.69.13-.634.085-1.29-.138-1.893.585-.274 1.084-.705 1.439-1.246.354-.54.551-1.17.569-1.816zM9.662 14.85l-3.429-3.428 1.293-1.302 2.072 2.072 4.4-4.794 1.347 1.246z"/></svg>' : ''}
      </div>
      <span class="handle-line">@${escapeHtml(tweet.author?.screen_name)} · ${time}</span>
    </div>
    <div class="logo">
      <svg viewBox="0 0 24 24"><path d="M18.244 2.25h3.308l-7.227 8.26 8.502 11.24H16.17l-5.214-6.817L4.99 21.75H1.68l7.73-8.835L1.254 2.25H8.08l4.713 6.231zm-1.161 17.52h1.833L7.084 4.126H5.117z"/></svg>
    </div>
  </div>
  <div class="text">${textHtml}</div>
  ${media}
  <div class="stats">
    <div class="stat replies">
      <svg viewBox="0 0 24 24"><path d="M1.751 10c0-4.42 3.584-8 8.005-8h4.366c4.49 0 8.129 3.64 8.129 8.13 0 2.96-1.607 5.68-4.196 7.11l-8.054 4.46v-3.69h-.067c-4.49.1-8.183-3.51-8.183-8.01zm8.005-6c-3.317 0-6.005 2.69-6.005 6 0 3.37 2.77 6.08 6.138 6.01l.351-.01h1.761v2.3l5.087-2.81c1.951-1.08 3.163-3.13 3.163-5.36 0-3.39-2.744-6.13-6.129-6.13H9.756z"/></svg>
      <span>${formatNumber(tweet.replies)}</span>
    </div>
    <div class="stat retweets">
      <svg viewBox="0 0 24 24"><path d="M4.5 3.88l4.432 4.14-1.364 1.46L5.5 7.55V16c0 1.1.896 2 2 2H13v2H7.5c-2.209 0-4-1.79-4-4V7.55L1.432 9.48.068 8.02 4.5 3.88zM16.5 6H11V4h5.5c2.209 0 4 1.79 4 4v8.45l2.068-1.93 1.364 1.46-4.432 4.14-4.432-4.14 1.364-1.46 2.068 1.93V8c0-1.1-.896-2-2-2z"/></svg>
      <span>${formatNumber(tweet.retweets)}</span>
    </div>
    <div class="stat likes">
      <svg viewBox="0 0 24 24"><path d="M16.697 5.5c-1.222-.06-2.679.51-3.89 2.16l-.805 1.09-.806-1.09C9.984 6.01 8.526 5.44 7.304 5.5c-1.243.07-2.349.78-2.91 1.91-.552 1.12-.633 2.78.479 4.82 1.074 1.97 3.257 4.27 7.129 6.61 3.87-2.34 6.052-4.64 7.126-6.61 1.111-2.04 1.03-3.7.477-4.82-.56-1.13-1.666-1.84-2.908-1.91zm4.187 7.69c-1.351 2.48-4.001 5.12-8.379 7.67l-.503.3-.504-.3c-4.379-2.55-7.029-5.19-8.382-7.67-1.36-2.5-1.41-4.86-.514-6.67.887-1.79 2.647-2.91 4.601-3.01 1.651-.09 3.368.56 4.798 2.01 1.429-1.45 3.146-2.1 4.796-2.01 1.954.1 3.714 1.22 4.601 3.01.896 1.81.846 4.17-.514 6.67z"/></svg>
      <span>${formatNumber(tweet.likes)}</span>
    </div>
    <div class="stat views">
      <svg viewBox="0 0 24 24"><path d="M8.75 21V3h2v18h-2zM18.75 21V8.5h2V21h-2zM13.75 21v-7.5h2V21h-2zM3.75 21v-3.5h2V21h-2z"/></svg>
      <span>${formatNumber(tweet.views)}</span>
    </div>
  </div>
  ${showWatermark ? `<div class="watermark">
    <div class="wm-left">
      ${logoB64 ? `<img class="wm-logo" src="${logoB64}" alt="" />` : ''}
      <div class="wm-text">
        ${WATERMARK_NAME ? `<span class="wm-name">${escapeHtml(WATERMARK_NAME)}</span>` : ''}
        ${WATERMARK_HANDLE ? `<span class="wm-handle">${escapeHtml(WATERMARK_HANDLE)}</span>` : ''}
      </div>
    </div>
    ${qrB64 ? `<img class="wm-qr" src="${qrB64}" alt="QR" />` : ''}
  </div>` : ''}
</div>
</body>
</html>`;
}

app.use(express.static(join(__dirname, 'public')));

app.get('/api/config', (req, res) => {
  res.json({
    watermarkAvailable: !!(logoB64 || WATERMARK_NAME),
    watermarkDefault: WATERMARK_ENABLED,
  });
});

app.get('/api/tweet', async (req, res) => {
  const { url } = req.query;
  if (!url) return res.status(400).json({ error: 'URL is required' });

  const parsed = parseTweetUrl(url);
  if (!parsed) return res.status(400).json({ error: 'Invalid tweet URL' });

  try {
    const apiUrl = `https://api.fxtwitter.com/${parsed.username}/status/${parsed.tweetId}`;
    const response = await fetch(apiUrl, {
      headers: { 'User-Agent': 'xshot/1.0' },
    });
    const data = await response.json();

    if (data.code !== 200 || !data.tweet) {
      return res.status(404).json({ error: 'Tweet not found' });
    }

    res.json({ tweet: data.tweet });
  } catch (err) {
    console.error('fxtwitter error:', err.message);
    res.status(502).json({ error: 'Failed to fetch tweet data' });
  }
});

app.get('/api/screenshot', async (req, res) => {
  const { url } = req.query;
  if (!url) return res.status(400).json({ error: 'URL is required' });

  const parsed = parseTweetUrl(url);
  if (!parsed) return res.status(400).json({ error: 'Invalid tweet URL' });

  try {
    const apiUrl = `https://api.fxtwitter.com/${parsed.username}/status/${parsed.tweetId}`;
    const response = await fetch(apiUrl, {
      headers: { 'User-Agent': 'xshot/1.0' },
    });
    const data = await response.json();

    if (data.code !== 200 || !data.tweet) {
      return res.status(404).json({ error: 'Tweet not found' });
    }

    const wm = req.query.watermark;
    const showWatermark = wm !== undefined ? wm !== '0' : WATERMARK_ENABLED;
    await embedImages(data.tweet);
    const html = buildTweetHtml(data.tweet, { showWatermark });
    const b = await getBrowser();
    const page = await b.newPage();

    try {
      await page.setViewport({ width: 600, height: 800, deviceScaleFactor: 2 });
      await page.setContent(html, { waitUntil: 'networkidle0', timeout: 15000 });
      const card = await page.$('#card');
      const png = await card.screenshot({ type: 'png', omitBackground: true });
      res.set('Content-Type', 'image/png');
      res.set('Content-Disposition', `inline; filename="tweet-${parsed.tweetId}.png"`);
      res.send(png);
    } finally {
      await page.close();
    }
  } catch (err) {
    console.error('Screenshot error:', err.message);
    res.status(500).json({ error: 'Failed to generate screenshot' });
  }
});

app.get('/api/video', async (req, res) => {
  const { url } = req.query;
  if (!url) return res.status(400).json({ error: 'URL is required' });

  let parsed;
  try {
    parsed = new URL(url);
  } catch {
    return res.status(400).json({ error: 'Invalid URL' });
  }

  if (parsed.protocol !== 'https:' || !ALLOWED_VIDEO_HOSTS.has(parsed.hostname)) {
    return res.status(403).json({ error: 'Forbidden host' });
  }

  try {
    const upstream = await fetch(url);
    if (!upstream.ok) {
      await upstream.body?.cancel();
      return res.status(upstream.status).json({ error: 'Upstream error' });
    }

    const contentType = upstream.headers.get('content-type') || 'video/mp4';
    const contentLength = upstream.headers.get('content-length');

    res.set('Content-Type', contentType);
    res.set('Content-Disposition', 'attachment; filename="video.mp4"');
    if (contentLength) res.set('Content-Length', contentLength);

    await pipeline(Readable.fromWeb(upstream.body), res);
  } catch (err) {
    if (!res.headersSent) {
      res.status(502).json({ error: 'Failed to fetch video' });
    }
  }
});

process.on('SIGINT', async () => {
  if (browser) await browser.close();
  process.exit(0);
});

app.listen(PORT, () => {
  console.log(`xshot -> http://localhost:${PORT}`);
});
