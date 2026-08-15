// OctoPort auth-screen background: a WebGL2 port of the React Bits
// "SlicedWaves" effect (ogl + GLSL) that runs with zero dependencies.
//
// Exposes:
//   window.__octoportSlicedWaves(canvasId, opts)  start the animation
//   window.__octoportSlicedWavesStop()            stop + release everything
//
// The fragment shader is ported verbatim from the original React component; the
// ogl renderer is replaced with a raw WebGL2 context + fullscreen triangle so
// no library needs to ship with the app.

(function () {
  'use strict';

  var VERT = '#version 300 es\n' +
    'in vec2 position;\n' +
    'void main() { gl_Position = vec4(position, 0.0, 1.0); }\n';

  var FRAG = '#version 300 es\n' +
    'precision highp float;\n' +
    'uniform vec2 iResolution;\n' +
    'uniform float iTime;\n' +
    'uniform float uColumns;\n' +
    'uniform float uRows;\n' +
    'uniform float uThickness;\n' +
    'uniform float uSpeed;\n' +
    'uniform float uTravel;\n' +
    'uniform float uWaveSpread;\n' +
    'uniform float uRowOffset;\n' +
    'uniform float uSoftness;\n' +
    'uniform float uGlow;\n' +
    'uniform float uBrightness;\n' +
    'uniform float uContrast;\n' +
    'uniform float uOpacity;\n' +
    'uniform float uVertical;\n' +
    'uniform float uAlternate;\n' +
    'uniform vec2 uMouse;\n' +
    'uniform float uMouseStrength;\n' +
    'uniform float uMouseRadius;\n' +
    'uniform float uEnableMouse;\n' +
    'uniform float uMouseActive;\n' +
    'uniform float uGrain;\n' +
    'uniform float uGrainIntensity;\n' +
    'uniform vec3 uColor1;\n' +
    'uniform vec3 uColor2;\n' +
    'uniform vec3 uColor3;\n' +
    'out vec4 fragColor;\n' +
    '\n' +
    'void main() {\n' +
    '  vec2 uv = gl_FragCoord.xy / iResolution.xy;\n' +
    '  vec2 grid = vec2(max(uColumns, 1.0), max(uRows, 1.0));\n' +
    '  vec2 p = uv * grid;\n' +
    '  vec2 gv = fract(p) - 0.5;\n' +
    '  vec2 id = floor(p);\n' +
    '\n' +
    '  float barCoord, waveId, offId, along;\n' +
    '  if (uVertical > 0.5) {\n' +
    '    barCoord = gv.x; waveId = id.y; offId = id.x; along = uv.y;\n' +
    '  } else {\n' +
    '    barCoord = gv.y; waveId = id.x; offId = id.y; along = uv.x;\n' +
    '  }\n' +
    '\n' +
    '  float dir = 1.0;\n' +
    '  if (uAlternate > 0.5 && mod(offId, 2.0) >= 1.0) dir = -1.0;\n' +
    '\n' +
    '  float phase = iTime * uSpeed + waveId * uWaveSpread + cos(offId * uRowOffset);\n' +
    '  float mv = sin(phase) * 0.5 + 0.5;\n' +
    '  if (dir < 0.0) mv = 1.0 - mv;\n' +
    '\n' +
    '  float infl = 0.0;\n' +
    '  if (uEnableMouse > 0.5) {\n' +
    '    float md = distance(uv, uMouse);\n' +
    '    infl = smoothstep(uMouseRadius, 0.0, md) * uMouseStrength * uMouseActive;\n' +
    '  }\n' +
    '\n' +
    '  float thick = clamp(uThickness + infl * 0.25, 0.0, 1.0);\n' +
    '  float startPos = (0.5 - thick * 0.5) * uTravel;\n' +
    '  float endPos = (-0.5 + thick * 0.5) * uTravel;\n' +
    '  float pos = mix(startPos, endPos, mv);\n' +
    '\n' +
    '  float aa = max(uSoftness, 0.0005);\n' +
    '  float d = abs(barCoord + pos) - thick * 0.5;\n' +
    '  float aaWidth = fwidth(uVertical > 0.5 ? p.x : p.y);\n' +
    '  float edge = max(aa, aaWidth);\n' +
    '  float mask = smoothstep(edge, -edge, d);\n' +
    '  float glow = exp(-max(d, 0.0) * (7.0 / (uGlow + 0.001))) * clamp(uGlow, 0.0, 1.0);\n' +
    '  float intensity = clamp(mask + glow * (1.0 - mask), 0.0, 1.0);\n' +
    '\n' +
    '  if (uGrain > 0.5) {\n' +
    '    float g = fract(sin(dot(gl_FragCoord.xy, vec2(12.9898, 78.233)) + iTime) * 43758.5453);\n' +
    '    intensity = clamp(intensity + (g - 0.5) * uGrainIntensity, 0.0, 1.0);\n' +
    '  }\n' +
    '\n' +
    '  float tint = mv;\n' +
    '  vec3 grad = mix(uColor2, uColor1, tint);\n' +
    '  grad = mix(grad, uColor3, clamp(along, 0.0, 1.0) * 0.45);\n' +
    '\n' +
    '  vec3 col = grad * uBrightness * (1.0 + infl * 0.6);\n' +
    '  col = (col - 0.5) * uContrast + 0.5;\n' +
    '  col = clamp(col, 0.0, 1.0);\n' +
    '\n' +
    '  float a = intensity * uOpacity;\n' +
    '  fragColor = vec4(col * a, a);\n' +
    '}\n';

  var hexToRgb = function (hex) {
    var result = /^#?([a-f\d]{2})([a-f\d]{2})([a-f\d]{2})$/i.exec(hex);
    if (!result) return [1, 1, 1];
    return [
      parseInt(result[1], 16) / 255,
      parseInt(result[2], 16) / 255,
      parseInt(result[3], 16) / 255,
    ];
  };

  var compile = function (gl, type, src) {
    var sh = gl.createShader(type);
    gl.shaderSource(sh, src);
    gl.compileShader(sh);
    if (!gl.getShaderParameter(sh, gl.COMPILE_STATUS)) {
      // eslint-disable-next-line no-console
      console.error('octoport waves shader error:', gl.getShaderInfoLog(sh));
      gl.deleteShader(sh);
      return null;
    }
    return sh;
  };

  // Set of per-canvas animation states; keeps every canvas alive for cleanup.
  var states = new Set();

  window.__octoportSlicedWaves = function (canvasId, opts) {
    var canvas = document.getElementById(canvasId);
    if (!canvas) return;
    if (canvas.dataset.octoportWaves === '1') return; // already running

    var o = opts || {};
    var props = {
      color1: o.color1 || '#FF9FFC',
      color2: o.color2 || '#5227FF',
      color3: o.color3 || '#B497CF',
      columns: o.columns != null ? o.columns : 14,
      rows: o.rows != null ? o.rows : 8,
      barThickness: o.barThickness != null ? o.barThickness : 0.1,
      speed: o.speed != null ? o.speed : 0.35,
      travel: o.travel != null ? o.travel : 0.7,
      waveSpread: o.waveSpread != null ? o.waveSpread : 0.9,
      rowOffset: o.rowOffset != null ? o.rowOffset : 1.0,
      softness: o.softness != null ? o.softness : 0.05,
      glow: o.glow != null ? o.glow : 0,
      brightness: o.brightness != null ? o.brightness : 1.0,
      contrast: o.contrast != null ? o.contrast : 1.0,
      opacity: o.opacity != null ? o.opacity : 0.5,
      orientation: o.orientation || 'horizontal',
      alternate: !!o.alternate,
      mouseInteraction: o.mouseInteraction !== false,
      mouseStrength: o.mouseStrength != null ? o.mouseStrength : 1,
      mouseRadius: o.mouseRadius != null ? o.mouseRadius : 0.3,
      grain: o.grain !== false,
      grainIntensity: o.grainIntensity != null ? o.grainIntensity : 0.05,
    };

    var gl = canvas.getContext('webgl2', {
      alpha: true,
      premultipliedAlpha: true,
      antialias: false,
    });
    if (!gl) {
      // WebGL2 unavailable: leave a subtle static gradient as a graceful fallback.
      canvas.style.background =
        'linear-gradient(160deg, ' + props.color1 + '22, ' + props.color2 + '33, ' + props.color3 + '22)';
      return;
    }
    gl.clearColor(0, 0, 0, 0);

    var vs = compile(gl, gl.VERTEX_SHADER, VERT);
    var fs = compile(gl, gl.FRAGMENT_SHADER, FRAG);
    if (!vs || !fs) return;

    var prog = gl.createProgram();
    gl.attachShader(prog, vs);
    gl.attachShader(prog, fs);
    gl.linkProgram(prog);
    if (!gl.getProgramParameter(prog, gl.LINK_STATUS)) {
      // eslint-disable-next-line no-console
      console.error('octoport waves link error:', gl.getProgramInfoLog(prog));
      return;
    }
    gl.useProgram(prog);

    var vbo = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, vbo);
    // Fullscreen triangle.
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 3, -1, -1, 3]), gl.STATIC_DRAW);
    var loc = gl.getAttribLocation(prog, 'position');
    gl.enableVertexAttribArray(loc);
    gl.vertexAttribPointer(loc, 2, gl.FLOAT, false, 0, 0);

    var u = {};
    [
      'iTime', 'iResolution', 'uColumns', 'uRows', 'uThickness', 'uSpeed',
      'uTravel', 'uWaveSpread', 'uRowOffset', 'uSoftness', 'uGlow',
      'uBrightness', 'uContrast', 'uOpacity', 'uVertical', 'uAlternate',
      'uMouse', 'uMouseStrength', 'uMouseRadius', 'uEnableMouse',
      'uMouseActive', 'uGrain', 'uGrainIntensity', 'uColor1', 'uColor2', 'uColor3',
    ].forEach(function (name) {
      u[name] = gl.getUniformLocation(prog, name);
    });

    var applyProps = function () {
      gl.uniform1f(u.uColumns, Math.max(1, Math.round(props.columns)));
      gl.uniform1f(u.uRows, Math.max(1, Math.round(props.rows)));
      gl.uniform1f(u.uThickness, props.barThickness);
      gl.uniform1f(u.uSpeed, props.speed);
      gl.uniform1f(u.uTravel, props.travel);
      gl.uniform1f(u.uWaveSpread, props.waveSpread);
      gl.uniform1f(u.uRowOffset, props.rowOffset);
      gl.uniform1f(u.uSoftness, props.softness);
      gl.uniform1f(u.uGlow, props.glow);
      gl.uniform1f(u.uBrightness, props.brightness);
      gl.uniform1f(u.uContrast, props.contrast);
      gl.uniform1f(u.uOpacity, props.opacity);
      gl.uniform1f(u.uVertical, props.orientation === 'vertical' ? 1.0 : 0.0);
      gl.uniform1f(u.uAlternate, props.alternate ? 1.0 : 0.0);
      gl.uniform1f(u.uMouseStrength, props.mouseStrength);
      gl.uniform1f(u.uMouseRadius, props.mouseRadius);
      gl.uniform1f(u.uEnableMouse, props.mouseInteraction ? 1.0 : 0.0);
      gl.uniform1f(u.uGrain, props.grain ? 1.0 : 0.0);
      gl.uniform1f(u.uGrainIntensity, props.grainIntensity);
      gl.uniform2f(u.uMouse, 0.5, 0.5);
      gl.uniform1f(u.uMouseActive, 0.0);
      var c1 = hexToRgb(props.color1);
      var c2 = hexToRgb(props.color2);
      var c3 = hexToRgb(props.color3);
      gl.uniform3f(u.uColor1, c1[0], c1[1], c1[2]);
      gl.uniform3f(u.uColor2, c2[0], c2[1], c2[2]);
      gl.uniform3f(u.uColor3, c3[0], c3[1], c3[2]);
    };
    applyProps();

    var setSize = function () {
      var rect = canvas.getBoundingClientRect();
      var w = Math.max(1, Math.floor(rect.width));
      var h = Math.max(1, Math.floor(rect.height));
      var dpr = Math.min(window.devicePixelRatio || 1, 2);
      var bw = Math.max(1, Math.floor(w * dpr));
      var bh = Math.max(1, Math.floor(h * dpr));
      canvas.width = bw;
      canvas.height = bh;
      gl.viewport(0, 0, bw, bh);
      gl.uniform2f(u.iResolution, bw, bh);
    };
    setSize();
    var ro = new ResizeObserver(setSize);
    ro.observe(canvas);

    var currentMouse = [0.5, 0.5];
    var targetMouse = [0.5, 0.5];
    var currentActive = 0;
    var targetActive = 0;

    var onMouseMove = function (e) {
      var rect = canvas.getBoundingClientRect();
      targetMouse = [
        (e.clientX - rect.left) / Math.max(rect.width, 1),
        1.0 - (e.clientY - rect.top) / Math.max(rect.height, 1),
      ];
      targetActive = 1;
    };
    var onMouseLeave = function () {
      targetActive = 0;
    };
    canvas.addEventListener('mousemove', onMouseMove);
    canvas.addEventListener('mouseleave', onMouseLeave);

    var raf = 0;
    var isVisible = true;
    var isPageVisible = !document.hidden;
    var t0 = performance.now();

    var loop = function (t) {
      gl.uniform1f(u.iTime, (t - t0) * 0.001);
      currentMouse[0] += 0.05 * (targetMouse[0] - currentMouse[0]);
      currentMouse[1] += 0.05 * (targetMouse[1] - currentMouse[1]);
      currentActive += 0.05 * (targetActive - currentActive);
      gl.uniform2f(u.uMouse, currentMouse[0], currentMouse[1]);
      gl.uniform1f(u.uMouseActive, currentActive);
      gl.drawArrays(gl.TRIANGLES, 0, 3);
      raf = requestAnimationFrame(loop);
    };

    var tryStart = function () {
      if (isVisible && isPageVisible && raf === 0) raf = requestAnimationFrame(loop);
    };
    var tryStop = function () {
      if (raf !== 0) {
        cancelAnimationFrame(raf);
        raf = 0;
      }
    };

    var io = new IntersectionObserver(
      function (entries) {
        isVisible = entries[0].isIntersecting;
        isVisible ? tryStart() : tryStop();
      },
      { threshold: 0 }
    );
    io.observe(canvas);

    var onVisibility = function () {
      isPageVisible = !document.hidden;
      isPageVisible ? tryStart() : tryStop();
    };
    document.addEventListener('visibilitychange', onVisibility);

    tryStart();

    canvas.dataset.octoportWaves = '1';
    states.add({
      gl: gl,
      ro: ro,
      io: io,
      raf: raf,
      onMouseMove: onMouseMove,
      onMouseLeave: onMouseLeave,
      onVisibility: onVisibility,
      canvas: canvas,
    });
  };

  window.__octoportSlicedWavesStop = function () {
    states.forEach(function (s) {
      if (s.raf !== 0) cancelAnimationFrame(s.raf);
      s.ro.disconnect();
      s.io.disconnect();
      document.removeEventListener('visibilitychange', s.onVisibility);
      s.canvas.removeEventListener('mousemove', s.onMouseMove);
      s.canvas.removeEventListener('mouseleave', s.onMouseLeave);
      try {
        s.gl.getExtension('WEBGL_lose_context');
      } catch (e) { /* ignore */ }
      delete s.canvas.dataset.octoportWaves;
    });
    states.clear();
  };
})();
