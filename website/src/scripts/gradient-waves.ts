/**
 * GradientWaves — vanilla TypeScript port of the React Bits <GradientWaves />
 * component (raymarched sine waves rolling toward a hazy horizon).
 *
 * Upstream renders through `ogl`; here the identical vertex and fragment
 * shaders are driven by raw WebGL2, so the site ships no framework runtime and
 * no extra dependency. The GLSL is unchanged.
 *
 * Behaviour added on top of the original:
 *   - renders a single static frame under `prefers-reduced-motion`
 *   - pauses the RAF loop off-screen and while the tab is hidden
 *   - degrades silently to nothing if WebGL2 is unavailable
 */

export interface GradientWavesOptions {
  horizonColor?: string;
  waveColor?: string;
  crestColor?: string;
  speed?: number;
  amplitude?: number;
  waveScale?: number;
  waveRatio?: number;
  swell?: number;
  turbulence?: number;
  tilt?: number;
  zoom?: number;
  height?: number;
  fogDepth?: number;
  detail?: 'low' | 'medium' | 'high';
  brightness?: number;
  opacity?: number;
  mouseInteraction?: boolean;
  parallaxStrength?: number;
  grain?: boolean;
  grainIntensity?: number;
}

const hexToRgb = (hex: string): [number, number, number] => {
  const m = /^#?([a-f\d]{2})([a-f\d]{2})([a-f\d]{2})$/i.exec(hex.trim());
  if (!m) return [1, 1, 1];
  return [parseInt(m[1], 16) / 255, parseInt(m[2], 16) / 255, parseInt(m[3], 16) / 255];
};

const detailToSteps = (d: string) => (d === 'low' ? 40.0 : d === 'high' ? 110.0 : 70.0);

const VERTEX = `#version 300 es
in vec2 position;
void main() {
  gl_Position = vec4(position, 0.0, 1.0);
}
`;

const FRAGMENT = `#version 300 es
precision highp float;
uniform vec2 iResolution;
uniform float iTime;
uniform float uSpeed;
uniform float uAmplitude;
uniform float uWaveScale;
uniform float uWaveRatio;
uniform float uSwell;
uniform float uTurbulence;
uniform float uTilt;
uniform float uZoom;
uniform float uHeight;
uniform float uFogDepth;
uniform float uSteps;
uniform float uBrightness;
uniform float uOpacity;
uniform float uGrain;
uniform float uGrainIntensity;
uniform vec2 uMouse;
uniform float uParallax;
uniform bool uEnableMouse;
uniform vec3 uHorizonColor;
uniform vec3 uWaveColor;
uniform vec3 uCrestColor;
out vec4 fragColor;

const float MAX_DIST = 20000.0;

float hash21(vec2 p) {
  vec3 p3 = fract(vec3(p.xyx) * 0.1031);
  p3 += dot(p3, p3.yzx + 33.33);
  return fract((p3.x + p3.y) * p3.z);
}

float plasma(vec3 r, vec2 freq, vec4 tc) {
  float mx = r.x + tc.x;
  mx += uSwell * sin((r.y + mx) / 20.0 + tc.y);
  float my = r.y - tc.z;
  my += uTurbulence * cos(r.x / 23.0 + tc.w);
  return r.z - (sin(mx * freq.x) * uAmplitude + sin(my * freq.y) * uAmplitude + uHeight);
}

float raymarch(vec3 pos, vec3 dir, vec2 freq, vec4 tc) {
  float dist = 0.0;
  for (int i = 0; i < 128; i++) {
    if (float(i) >= uSteps) break;
    float dscene = plasma(pos + dist * dir, freq, tc);
    if (abs(dscene) < 0.1) break;
    dist += 0.9 * dscene;
    if (!(abs(dist) < MAX_DIST)) return MAX_DIST;
  }
  return dist;
}

void main() {
  float T = iTime * uSpeed;
  vec2 freq = vec2(uWaveScale / 7.0, (uWaveScale * uWaveRatio) / 3.0);
  vec4 tc = vec4(T / 0.130, T / 0.810, T / 0.200, T / 0.710);
  float c, s;
  float vfov = (3.14159 / 2.3) / max(uZoom, 0.05);
  vec3 cam = vec3(0.0, 0.0, 30.0);
  vec2 uv = (gl_FragCoord.xy / iResolution.xy) - 0.5;
  uv.x *= iResolution.x / iResolution.y;
  uv.y *= -1.0;

  vec3 dir = vec3(0.0, 0.0, -1.0);
  float ulen = length(uv);
  float xrot = vfov * ulen;
  c = cos(xrot); s = sin(xrot);
  dir = mat3(1.0, 0.0, 0.0, 0.0, c, -s, 0.0, s, c) * dir;
  vec2 nuv = ulen > 1e-5 ? uv / ulen : vec2(1.0, 0.0);
  c = nuv.x; s = nuv.y;
  dir = mat3(c, -s, 0.0, s, c, 0.0, 0.0, 0.0, 1.0) * dir;
  c = cos(uTilt); s = sin(uTilt);
  dir = mat3(c, 0.0, s, 0.0, 1.0, 0.0, -s, 0.0, c) * dir;

  if (uEnableMouse) {
    float yaw = (uMouse.x - 0.5) * uParallax * 0.4;
    float pitch = (uMouse.y - 0.5) * uParallax * 0.4;
    c = cos(yaw); s = sin(yaw);
    dir = mat3(c, 0.0, s, 0.0, 1.0, 0.0, -s, 0.0, c) * dir;
    c = cos(pitch); s = sin(pitch);
    dir = mat3(1.0, 0.0, 0.0, 0.0, c, -s, 0.0, s, c) * dir;
  }

  float dist = raymarch(cam, dir, freq, tc);
  vec3 pos = cam + dist * dir;

  float t = clamp(uFogDepth / max(dist, 0.001), 0.0, 1.0);
  vec3 body = mix(uWaveColor, uCrestColor, clamp(pos.z * 0.08 + 0.5, 0.0, 1.0));
  vec3 col = mix(uHorizonColor, body, t);
  col *= uBrightness;
  col = clamp(col, 0.0, 1.0);

  float alpha = clamp(t, 0.0, 1.0) * uOpacity;
  if (uGrain > 0.5) {
    float g = hash21(gl_FragCoord.xy + mod(iTime, 64.0) * 11.0);
    alpha += (g - 0.5) * uGrainIntensity;
  }
  alpha = clamp(alpha, 0.0, 1.0);
  fragColor = vec4(col * alpha, alpha);
}
`;

export function mountGradientWaves(container: HTMLElement, opts: GradientWavesOptions = {}) {
  const noop = () => {};
  const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  const canvas = document.createElement('canvas');
  canvas.setAttribute('aria-hidden', 'true');
  canvas.style.width = '100%';
  canvas.style.height = '100%';
  canvas.style.display = 'block';
  container.appendChild(canvas);

  const gl = canvas.getContext('webgl2', {
    alpha: true,
    premultipliedAlpha: true,
    antialias: false,
  });
  if (!gl) {
    canvas.remove();
    return noop;
  }

  function compile(type: number, src: string) {
    const sh = gl!.createShader(type)!;
    gl!.shaderSource(sh, src);
    gl!.compileShader(sh);
    if (!gl!.getShaderParameter(sh, gl!.COMPILE_STATUS)) {
      gl!.deleteShader(sh);
      return null;
    }
    return sh;
  }

  const vs = compile(gl.VERTEX_SHADER, VERTEX);
  const fs = compile(gl.FRAGMENT_SHADER, FRAGMENT);
  if (!vs || !fs) {
    canvas.remove();
    return noop;
  }

  const prog = gl.createProgram()!;
  gl.attachShader(prog, vs);
  gl.attachShader(prog, fs);
  gl.linkProgram(prog);
  if (!gl.getProgramParameter(prog, gl.LINK_STATUS)) {
    canvas.remove();
    return noop;
  }
  gl.useProgram(prog);

  const vao = gl.createVertexArray();
  gl.bindVertexArray(vao);
  const buf = gl.createBuffer();
  gl.bindBuffer(gl.ARRAY_BUFFER, buf);
  gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 3, -1, -1, 3]), gl.STATIC_DRAW);
  const posLoc = gl.getAttribLocation(prog, 'position');
  gl.enableVertexAttribArray(posLoc);
  gl.vertexAttribPointer(posLoc, 2, gl.FLOAT, false, 0, 0);

  const u = (n: string) => gl.getUniformLocation(prog, n);

  const {
    horizonColor = '#5227FF',
    waveColor = '#CCCCFF',
    crestColor = '#FFFFFF',
    speed = 0.4,
    amplitude = 2.5,
    waveScale = 0.6,
    waveRatio = 0.9,
    swell = 35,
    turbulence = 20,
    tilt = 1.11,
    zoom = 1.0,
    height = 5.5,
    fogDepth = 15,
    detail = 'medium',
    brightness = 1.0,
    opacity = 1.0,
    mouseInteraction = true,
    parallaxStrength = 0.5,
    grain = true,
    grainIntensity = 0.05,
  } = opts;

  gl.uniform1f(u('uSpeed'), speed);
  gl.uniform1f(u('uAmplitude'), amplitude);
  gl.uniform1f(u('uWaveScale'), waveScale);
  gl.uniform1f(u('uWaveRatio'), waveRatio);
  gl.uniform1f(u('uSwell'), swell);
  gl.uniform1f(u('uTurbulence'), turbulence);
  gl.uniform1f(u('uTilt'), tilt);
  gl.uniform1f(u('uZoom'), zoom);
  gl.uniform1f(u('uHeight'), height);
  gl.uniform1f(u('uFogDepth'), fogDepth);
  gl.uniform1f(u('uSteps'), detailToSteps(detail));
  gl.uniform1f(u('uBrightness'), brightness);
  gl.uniform1f(u('uOpacity'), opacity);
  gl.uniform1f(u('uGrain'), grain ? 1.0 : 0.0);
  gl.uniform1f(u('uGrainIntensity'), grainIntensity);
  gl.uniform1f(u('uParallax'), parallaxStrength);
  gl.uniform1i(u('uEnableMouse'), mouseInteraction ? 1 : 0);

  const c1 = hexToRgb(horizonColor);
  const c2 = hexToRgb(waveColor);
  const c3 = hexToRgb(crestColor);
  gl.uniform3f(u('uHorizonColor'), c1[0], c1[1], c1[2]);
  gl.uniform3f(u('uWaveColor'), c2[0], c2[1], c2[2]);
  gl.uniform3f(u('uCrestColor'), c3[0], c3[1], c3[2]);

  const uRes = u('iResolution');
  const uTime = u('iTime');
  const uMouse = u('uMouse');

  gl.clearColor(0, 0, 0, 0);
  gl.enable(gl.BLEND);
  gl.blendFunc(gl.ONE, gl.ONE_MINUS_SRC_ALPHA);

  // The raymarch is the expensive part of this shader, so cap the backing
  // store at 1.5x rather than the usual 2x: on a hero-sized canvas the extra
  // pixels cost far more than they show.
  const dpr = Math.min(window.devicePixelRatio || 1, 1.5);

  function draw(t: number) {
    gl!.uniform1f(uTime, t);
    gl!.uniform2f(uMouse, current[0], current[1]);
    gl!.clear(gl!.COLOR_BUFFER_BIT);
    gl!.drawArrays(gl!.TRIANGLES, 0, 3);
  }

  function setSize() {
    const rect = container.getBoundingClientRect();
    const w = Math.max(1, Math.floor(rect.width * dpr));
    const h = Math.max(1, Math.floor(rect.height * dpr));
    if (canvas.width === w && canvas.height === h) return;
    canvas.width = w;
    canvas.height = h;
    gl!.viewport(0, 0, w, h);
    gl!.uniform2f(uRes, w, h);
    if (reduceMotion) draw(0);
  }

  const ro = new ResizeObserver(setSize);
  ro.observe(container);
  setSize();

  const target = [0.5, 0.5];
  const current = [0.5, 0.5];
  const onMove = (e: PointerEvent) => {
    const rect = canvas.getBoundingClientRect();
    target[0] = (e.clientX - rect.left) / rect.width;
    target[1] = 1.0 - (e.clientY - rect.top) / rect.height;
  };
  const onLeave = () => {
    target[0] = 0.5;
    target[1] = 0.5;
  };
  // Listen on the window: the canvas is pointer-events:none so the page stays
  // fully interactive underneath the backdrop.
  if (mouseInteraction) {
    window.addEventListener('pointermove', onMove, { passive: true });
    document.addEventListener('pointerleave', onLeave);
  }

  let raf = 0;
  let t0 = performance.now();
  let inView = true;
  let pageVisible = !document.hidden;

  function loop(t: number) {
    current[0] += 0.05 * (target[0] - current[0]);
    current[1] += 0.05 * (target[1] - current[1]);
    draw((t - t0) * 0.001);
    raf = requestAnimationFrame(loop);
  }
  function start() {
    if (reduceMotion || raf !== 0) return;
    if (inView && pageVisible) raf = requestAnimationFrame(loop);
  }
  function stop() {
    if (raf !== 0) {
      cancelAnimationFrame(raf);
      raf = 0;
    }
  }

  const io = new IntersectionObserver(
    ([e]) => {
      inView = e.isIntersecting;
      inView ? start() : stop();
    },
    { threshold: 0 },
  );
  io.observe(container);

  const onVisibility = () => {
    pageVisible = !document.hidden;
    pageVisible ? start() : stop();
  };
  document.addEventListener('visibilitychange', onVisibility);

  if (reduceMotion) draw(0);
  else start();

  return function destroy() {
    stop();
    ro.disconnect();
    io.disconnect();
    document.removeEventListener('visibilitychange', onVisibility);
    window.removeEventListener('pointermove', onMove);
    document.removeEventListener('pointerleave', onLeave);
    gl!.getExtension('WEBGL_lose_context')?.loseContext();
    canvas.remove();
  };
}
