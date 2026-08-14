/**
 * MoltenMetal — vanilla TypeScript port of the React Bits <MoltenMetal />
 * component.
 *
 * Upstream renders through the `ogl` package; here the identical vertex and
 * fragment shaders are driven by raw WebGL2, so the site keeps shipping no
 * framework runtime and no extra dependency. The GLSL is unchanged.
 *
 * Behaviour added on top of the original:
 *   - skips entirely under `prefers-reduced-motion` (renders one static frame)
 *   - pauses the RAF loop off-screen and while the tab is hidden
 *   - re-reads its palette when the site theme flips
 *   - degrades silently to nothing if WebGL2 is unavailable
 */

export interface MoltenMetalOptions {
  color1?: string;
  color2?: string;
  color3?: string;
  speed?: number;
  scale?: number;
  detail?: number;
  glow?: number;
  coreSize?: number;
  swirl?: number;
  fold?: number;
  blackPoint?: number;
  brightness?: number;
  colorMode?: 'molten' | 'ember' | 'frost';
  grain?: boolean;
  grainIntensity?: number;
  mouseInteraction?: boolean;
  mouseStrength?: number;
  opacity?: number;
}

const hexToRgb = (hex: string): [number, number, number] => {
  const m = /^#?([a-f\d]{2})([a-f\d]{2})([a-f\d]{2})$/i.exec(hex.trim());
  if (!m) return [1, 1, 1];
  return [parseInt(m[1], 16) / 255, parseInt(m[2], 16) / 255, parseInt(m[3], 16) / 255];
};

const colorModeToFloat = (mode: string) => (mode === 'ember' ? 1 : mode === 'frost' ? 2 : 0);

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
uniform float uScale;
uniform float uDetail;
uniform float uGlow;
uniform float uCoreSize;
uniform float uSwirl;
uniform float uFold;
uniform float uBlackPoint;
uniform float uBrightness;
uniform float uColorMode;
uniform float uGrain;
uniform float uGrainIntensity;
uniform float uOpacity;
uniform vec2 uMouse;
uniform float uMouseStrength;
uniform bool uEnableMouse;
uniform vec3 uColor1;
uniform vec3 uColor2;
uniform vec3 uColor3;
out vec4 fragColor;

float hash(vec2 p) {
  return fract(sin(dot(p, vec2(12.9898, 78.233))) * 43758.5453);
}

void main() {
  float time = iTime * uSpeed;
  vec2 p = uScale * ((gl_FragCoord.xy - 0.5 * iResolution.xy) / iResolution.y) - 0.5;

  vec2 drift = vec2(0.0);
  if (uEnableMouse) {
    drift = (uMouse - 0.5) * uMouseStrength * 2.0;
  }
  p += drift;

  vec2 i = p;
  float c = 0.0;
  float r = length(p + vec2(sin(time), sin(time * 0.3 + 5.0)) * 0.5);
  float d = length(p);
  float rot = d + time + p.x * uSwirl;

  float cosRot = cos(rot);
  mat2 warp = mat2(cos(rot - sin(time / 5.0)), sin(rot), -sin(cosRot - time), cosRot) * uFold;
  float glowCore = uGlow * uCoreSize;

  for (float n = 0.0; n < 8.0; n++) {
    if (n >= uDetail) break;
    p *= warp;
    float t = r - time / (n + 3.0);
    i -= p + vec2(cos(t - i.x - r) + sin(t + i.y), sin(t - i.y) + cos(t + i.x) + r);
    c += glowCore / length(vec2(sin(i.x + t), cos(i.y + t)));
  }

  c /= 6.0;

  float intensity = max(c - uBlackPoint, 0.0) * uBrightness;

  float g = clamp(intensity, 0.0, 1.0);

  float mid = 0.5;
  if (uColorMode > 1.5) {
    mid = 0.65;
  } else if (uColorMode > 0.5) {
    mid = 0.35;
  }

  vec3 col = mix(uColor1, uColor2, smoothstep(0.0, mid, g));
  col = mix(col, uColor3, smoothstep(mid, 1.0, g));

  float a = g;
  if (uGrain > 0.5) {
    float gr = hash(gl_FragCoord.xy + iTime);
    a += (gr - 0.5) * uGrainIntensity;
  }
  a = clamp(a, 0.0, 1.0) * uOpacity;
  fragColor = vec4(col * a, a);
}
`;

/** Read a themed colour from the element's computed custom properties. */
function themed(el: Element, name: string, fallback: string): string {
  const v = getComputedStyle(el).getPropertyValue(name).trim();
  return v || fallback;
}

export function mountMoltenMetal(container: HTMLElement, opts: MoltenMetalOptions = {}) {
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
  // No WebGL2 (older Safari, blocked GPU, headless): leave the container empty.
  if (!gl) {
    canvas.remove();
    return noop;
  }

  function compile(type: number, src: string) {
    const s = gl!.createShader(type)!;
    gl!.shaderSource(s, src);
    gl!.compileShader(s);
    if (!gl!.getShaderParameter(s, gl!.COMPILE_STATUS)) {
      gl!.deleteShader(s);
      return null;
    }
    return s;
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

  // Full-screen triangle (matches ogl's Triangle geometry).
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
    speed = 0.35,
    scale = 4,
    detail = 3,
    glow = 1.6,
    coreSize = 0.1,
    swirl = 1,
    fold = -0.2,
    blackPoint = 0.05,
    brightness = 1.3,
    colorMode = 'molten',
    grain = true,
    grainIntensity = 0.05,
    mouseInteraction = true,
    mouseStrength = 0.3,
    opacity = 1.0,
  } = opts;

  gl.uniform1f(u('uSpeed'), speed);
  gl.uniform1f(u('uScale'), scale);
  gl.uniform1f(u('uDetail'), detail);
  gl.uniform1f(u('uGlow'), glow);
  gl.uniform1f(u('uCoreSize'), Math.max(coreSize, 0.001));
  gl.uniform1f(u('uSwirl'), swirl);
  gl.uniform1f(u('uFold'), fold);
  gl.uniform1f(u('uBlackPoint'), blackPoint);
  gl.uniform1f(u('uBrightness'), brightness);
  gl.uniform1f(u('uColorMode'), colorModeToFloat(colorMode));
  gl.uniform1f(u('uGrain'), grain ? 1 : 0);
  gl.uniform1f(u('uGrainIntensity'), grainIntensity);
  gl.uniform1i(u('uEnableMouse'), mouseInteraction ? 1 : 0);
  gl.uniform1f(u('uMouseStrength'), mouseStrength);

  const uOpacity = u('uOpacity');
  const uRes = u('iResolution');
  const uTime = u('iTime');
  const uMouse = u('uMouse');
  const uC1 = u('uColor1');
  const uC2 = u('uColor2');
  const uC3 = u('uColor3');

  /**
   * The palette and overall opacity come from CSS custom properties so the
   * effect can differ between light and dark themes without a second shader.
   */
  function readPalette() {
    const c1 = hexToRgb(opts.color1 ?? themed(container, '--molten-1', '#5227FF'));
    const c2 = hexToRgb(opts.color2 ?? themed(container, '--molten-2', '#FF9FFC'));
    const c3 = hexToRgb(opts.color3 ?? themed(container, '--molten-3', '#FFFFFF'));
    gl!.uniform3f(uC1, c1[0], c1[1], c1[2]);
    gl!.uniform3f(uC2, c2[0], c2[1], c2[2]);
    gl!.uniform3f(uC3, c3[0], c3[1], c3[2]);
    const o = parseFloat(themed(container, '--molten-opacity', '')) || opacity;
    gl!.uniform1f(uOpacity, o);
  }
  readPalette();

  gl.clearColor(0, 0, 0, 0);
  gl.enable(gl.BLEND);
  gl.blendFunc(gl.ONE, gl.ONE_MINUS_SRC_ALPHA);

  const dpr = Math.min(window.devicePixelRatio || 1, 2);
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
  const onMouseMove = (e: MouseEvent) => {
    const rect = canvas.getBoundingClientRect();
    target[0] = (e.clientX - rect.left) / rect.width;
    target[1] = 1.0 - (e.clientY - rect.top) / rect.height;
  };
  const onMouseLeave = () => {
    target[0] = 0.5;
    target[1] = 0.5;
  };
  // Listen on the window: the canvas itself is pointer-events:none so the
  // page stays fully interactive underneath the backdrop.
  if (mouseInteraction) {
    window.addEventListener('mousemove', onMouseMove, { passive: true });
    document.addEventListener('mouseleave', onMouseLeave);
  }

  function draw(t: number) {
    gl!.uniform1f(uTime, t);
    gl!.uniform2f(uMouse, current[0], current[1]);
    gl!.clear(gl!.COLOR_BUFFER_BIT);
    gl!.drawArrays(gl!.TRIANGLES, 0, 3);
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
    ([entry]) => {
      inView = entry.isIntersecting;
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

  const themeObserver = new MutationObserver(() => {
    readPalette();
    if (reduceMotion) draw(0);
  });
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] });

  if (reduceMotion) draw(0);
  else start();

  return function destroy() {
    stop();
    ro.disconnect();
    io.disconnect();
    themeObserver.disconnect();
    document.removeEventListener('visibilitychange', onVisibility);
    window.removeEventListener('mousemove', onMouseMove);
    document.removeEventListener('mouseleave', onMouseLeave);
    gl!.getExtension('WEBGL_lose_context')?.loseContext();
    canvas.remove();
  };
}
