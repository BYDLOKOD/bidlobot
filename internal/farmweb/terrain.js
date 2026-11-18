// The canvas is a 320×208 pixel-art scene. Native tile buttons sit over its plots.
function paintYard(canvas, tiles, yard, now) {
  const ctx = canvas.getContext('2d');
  if (!ctx) return;
  function rect(x, y, w, h, color) {
    ctx.fillStyle = color;
    ctx.fillRect(x, y, w, h);
  }
  let seed = 113 + (yard?.yard_x ?? 0) * 211 + (yard?.yard_y ?? 0) * 19;
  function rand(max) {
    seed = (Math.imul(1664525, seed) + 1013904223) >>> 0;
    return (seed >>> 16) % max;
  }
  function tree(x, y) {
    rect(x + 2, y + 15, 18, 6, '#507847');
    rect(x + 9, y + 9, 4, 11, '#67452f');
    rect(x + 1, y + 6, 19, 8, '#315a3e');
    rect(x + 4, y + 2, 14, 14, '#315a3e');
    rect(x + 4, y + 4, 13, 8, '#427443');
    rect(x + 6, y + 2, 10, 5, '#508a48');
    rect(x + 6, y + 5, 4, 2, '#86a84f');
    rect(x + 13, y + 9, 2, 2, '#bd6c43');
  }
  function cabin(x, y) {
    rect(x + 3, y + 22, 39, 8, '#52794a');
    rect(x + 5, y + 7, 30, 19, '#71472f');
    rect(x + 7, y + 9, 26, 15, '#c69055');
    for (let i = 13; i < 24; i += 4) rect(x + 7, y + i, 26, 1, '#a46a43');
    rect(x + 10, y + 12, 7, 6, '#34454b');
    rect(x + 11, y + 13, 5, 4, '#f0cd79');
    rect(x + 22, y + 13, 8, 13, '#593b32');
    rect(x + 28, y + 19, 1, 1, '#e8be67');
    rect(x + 3, y + 3, 34, 7, '#854239');
    rect(x + 7, y, 26, 4, '#b45c43');
    rect(x + 3, y + 4, 33, 1, '#d2784d');
    rect(x + 2, y + 10, 36, 2, '#593b32');
  }
  function building(x, y, kind) {
    const mill = kind === 'mill';
    rect(x - 14, y + 10, 30, 4, '#634c35');
    rect(x - 10, y - 7, 20, 18, mill ? '#ded1a9' : '#a65543');
    rect(x - 12, y - 9, 24, 5, mill ? '#624b40' : '#854239');
    rect(x - 7, y - 12, 14, 3, mill ? '#a27656' : '#bd7053');
    rect(x - 4, y + 3, 8, 8, '#43352c');
    if (mill) {
      rect(x - 2, y - 15, 4, 20, '#573e32');
      rect(x - 10, y - 7, 20, 4, '#573e32');
      rect(x - 1, y - 14, 3, 8, '#f5deb0');
      rect(x - 9, y - 6, 8, 3, '#f5deb0');
      rect(x + 2, y - 7, 7, 3, '#f5deb0');
      rect(x - 2, y - 3, 3, 7, '#f5deb0');
    } else {
      rect(x + 5, y - 16, 4, 7, '#854239');
      rect(x - 2, y + 7, 5, 3, '#eb9e4e');
    }
  }
  rect(0, 0, 320, 208, '#557d4a');
  rect(4, 5, 312, 197, '#71974e');
  for (let i = 0; i < 480; i++) {
    const x = rand(312) + 4, y = rand(196) + 5;
    rect(x, y, 1 + rand(4), 1 + rand(2), ['#7ca155', '#668c48', '#84a65b', '#5f8544'][rand(4)]);
  }
  // Pond, edges and stepping stones.
  rect(234, 18, 65, 35, '#567b47');
  rect(230, 25, 72, 22, '#567b47');
  rect(239, 21, 55, 30, '#406d68');
  rect(234, 26, 65, 20, '#406d68');
  rect(240, 24, 53, 24, '#61938d');
  rect(237, 28, 60, 13, '#61938d');
  for (const [x, y, length] of [[247, 29, 9], [274, 28, 13], [264, 37, 11], [244, 42, 5], [282, 40, 7]]) {
    rect(x, y, length, 1, '#94b9a1');
  }
  for (const [x, y] of [[247, 36], [269, 31], [289, 43]]) {
    rect(x, y, 5, 3, '#3e7252');
    rect(x + 2, y, 2, 1, '#ead29c');
  }
  rect(0, 54, 320, 20, '#648748');
  rect(0, 58, 320, 12, '#b59262');
  rect(49, 45, 16, 155, '#b59262');
  rect(55, 187, 235, 13, '#b59262');
  rect(273, 59, 12, 136, '#b59262');
  for (let i = 0; i < 35; i++) {
    rect(rand(318), 59 + rand(10), 2, 1, '#d0ac73');
    rect(50 + rand(14), 75 + rand(110), 2, 1, '#d0ac73');
  }
  cabin(31, 21);
  for (const [x, y] of [[5, 17], [7, 37], [82, 17], [103, 12], [124, 22], [159, 11], [207, 17], [293, 9], [289, 63], [295, 82], [288, 110], [294, 144], [286, 172], [11, 99], [16, 136], [6, 157], [16, 177]]) tree(x, y);
  for (const y of [82, 121, 161]) {
    rect(28, y + 5, 15, 2, '#85633f');
    rect(28, y + 10, 15, 2, '#85633f');
    rect(29, y + 3, 3, 12, '#a47b49');
    rect(39, y + 3, 3, 12, '#a47b49');
  }
  for (const tile of tiles) {
    const x = 75 + tile.x * 66, y = 81 + tile.y * 39;
    if (tile.state === 'unbuilt' || tile.state === 'locked') {
      rect(x, y, 57, 30, '#608648');
      rect(x + 2, y + 2, 53, 26, '#799c55');
      for (let i = 4; i < 54; i += 7) {
        rect(x + i, y, 3, 2, '#b6ac70');
        rect(x + i, y + 28, 3, 2, '#b6ac70');
      }
      continue;
    }
    if (tile.kind === 'plot') {
      rect(x, y, 57, 33, '#50382e');
      rect(x + 2, y + 2, 53, 29, '#81513b');
      for (let i = 5; i < 30; i += 7) {
        rect(x + 4, y + i, 49, 2, '#55382f');
        rect(x + 5, y + i + 2, 46, 1, '#a16947');
      }
      if (tile.job) {
        const progress = (now - tile.job.start) / (tile.job.end - tile.job.start);
        const ripe = tile.state === 'ready' || progress >= .67;
        const height = ripe ? 8 : progress >= .33 ? 6 : 4;
        for (let row = 0; row < 2; row++) {
          for (let i = 0; i < 5; i++) {
            const cx = x + 8 + i * 10 + row * 2, cy = y + 12 + row * 9;
            rect(cx, cy - height, 2, height, ripe ? '#d8ab52' : '#9eb457');
            rect(cx - 2, cy - height + 2, 2, 2, ripe ? '#f0c36b' : '#c2c76a');
            rect(cx + 2, cy - height, 2, 2, ripe ? '#edca73' : '#8d9f4e');
          }
        }
      }
    } else {
      rect(x, y + 4, 57, 27, '#866a47');
      rect(x + 2, y + 5, 53, 23, '#b29365');
      if (tile.state === 'building') {
        for (let i = 13; i < 46; i += 9) rect(x + i, y + 5, 2, 20, '#69472e');
        rect(x + 11, y + 9, 34, 3, '#d1aa69');
        rect(x + 11, y + 16, 34, 3, '#d1aa69');
      } else {
        building(x + 28, y + 12, tile.kind);
      }
    }
  }
}
