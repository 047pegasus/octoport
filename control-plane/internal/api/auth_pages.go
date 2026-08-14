package api

// Shared presentation for the browser-facing auth pages (the `octoport login`
// sign-in screen, the "signed in" confirmation, and the GitHub OAuth result
// pages). Keeping the chrome in one place means the three pages cannot drift
// apart visually.
//
// The animated backdrop is a port of the React Bits <SlicedWaves /> component.
// Upstream renders through the `ogl` package; here the same fragment shader is
// driven by ~60 lines of raw WebGL2 so these pages stay dependency-free static
// HTML with no build step. The shader body is unchanged.

// authBackgroundCSS styles the fixed-position canvas that sits behind the card.
const authBackgroundCSS = `
  #bg{position:fixed;inset:0;width:100%;height:100%;z-index:0;display:block;pointer-events:none}
  .card,.shell{position:relative;z-index:1}
`

// authBackgroundHTML is the canvas plus the WebGL2 renderer. It degrades to an
// empty (transparent) canvas if the context cannot be created, and it never
// throws — the sign-in form must work even when the effect does not.
const authBackgroundHTML = `
<canvas id="bg" aria-hidden="true"></canvas>
<script>
(function () {
  var canvas = document.getElementById('bg');
  if (!canvas) return;

  // Respect reduced-motion and skip the effect entirely on those machines.
  if (window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;

  var gl = canvas.getContext('webgl2', { alpha: true, premultipliedAlpha: true, antialias: false });
  if (!gl) return;

  var VERT = '#version 300 es\n' +
    'in vec2 position;\n' +
    'void main(){ gl_Position = vec4(position, 0.0, 1.0); }\n';

  var FRAG = '#version 300 es\n' +
    'precision highp float;\n' +
    'uniform vec2 iResolution; uniform float iTime;\n' +
    'uniform float uColumns; uniform float uRows; uniform float uThickness;\n' +
    'uniform float uSpeed; uniform float uTravel; uniform float uWaveSpread;\n' +
    'uniform float uRowOffset; uniform float uSoftness; uniform float uGlow;\n' +
    'uniform float uBrightness; uniform float uContrast; uniform float uOpacity;\n' +
    'uniform float uVertical; uniform float uAlternate;\n' +
    'uniform vec2 uMouse; uniform float uMouseStrength; uniform float uMouseRadius;\n' +
    'uniform float uEnableMouse; uniform float uMouseActive;\n' +
    'uniform float uGrain; uniform float uGrainIntensity;\n' +
    'uniform vec3 uColor1; uniform vec3 uColor2; uniform vec3 uColor3;\n' +
    'out vec4 fragColor;\n' +
    'void main(){\n' +
    '  vec2 uv = gl_FragCoord.xy / iResolution.xy;\n' +
    '  vec2 grid = vec2(max(uColumns,1.0), max(uRows,1.0));\n' +
    '  vec2 p = uv * grid; vec2 gv = fract(p) - 0.5; vec2 id = floor(p);\n' +
    '  float barCoord, waveId, offId, along;\n' +
    '  if (uVertical > 0.5) { barCoord = gv.x; waveId = id.y; offId = id.x; along = uv.y; }\n' +
    '  else { barCoord = gv.y; waveId = id.x; offId = id.y; along = uv.x; }\n' +
    '  float dir = 1.0;\n' +
    '  if (uAlternate > 0.5 && mod(offId, 2.0) >= 1.0) dir = -1.0;\n' +
    '  float phase = iTime * uSpeed + waveId * uWaveSpread + cos(offId * uRowOffset);\n' +
    '  float mv = sin(phase) * 0.5 + 0.5;\n' +
    '  if (dir < 0.0) mv = 1.0 - mv;\n' +
    '  float infl = 0.0;\n' +
    '  if (uEnableMouse > 0.5) {\n' +
    '    float md = distance(uv, uMouse);\n' +
    '    infl = smoothstep(uMouseRadius, 0.0, md) * uMouseStrength * uMouseActive;\n' +
    '  }\n' +
    '  float thick = clamp(uThickness + infl * 0.25, 0.0, 1.0);\n' +
    '  float startPos = (0.5 - thick * 0.5) * uTravel;\n' +
    '  float endPos = (-0.5 + thick * 0.5) * uTravel;\n' +
    '  float pos = mix(startPos, endPos, mv);\n' +
    '  float aa = max(uSoftness, 0.0005);\n' +
    '  float d = abs(barCoord + pos) - thick * 0.5;\n' +
    '  float aaWidth = fwidth(uVertical > 0.5 ? p.x : p.y);\n' +
    '  float edge = max(aa, aaWidth);\n' +
    '  float mask = smoothstep(edge, -edge, d);\n' +
    '  float glow = exp(-max(d, 0.0) * (7.0 / (uGlow + 0.001))) * clamp(uGlow, 0.0, 1.0);\n' +
    '  float intensity = clamp(mask + glow * (1.0 - mask), 0.0, 1.0);\n' +
    '  if (uGrain > 0.5) {\n' +
    '    float g = fract(sin(dot(gl_FragCoord.xy, vec2(12.9898,78.233)) + iTime) * 43758.5453);\n' +
    '    intensity = clamp(intensity + (g - 0.5) * uGrainIntensity, 0.0, 1.0);\n' +
    '  }\n' +
    '  float tint = mv;\n' +
    '  vec3 grad = mix(uColor2, uColor1, tint);\n' +
    '  grad = mix(grad, uColor3, clamp(along, 0.0, 1.0) * 0.45);\n' +
    '  vec3 col = grad * uBrightness * (1.0 + infl * 0.6);\n' +
    '  col = (col - 0.5) * uContrast + 0.5;\n' +
    '  col = clamp(col, 0.0, 1.0);\n' +
    '  float a = intensity * uOpacity;\n' +
    '  fragColor = vec4(col * a, a);\n' +
    '}\n';

  function compile(type, src) {
    var s = gl.createShader(type);
    gl.shaderSource(s, src);
    gl.compileShader(s);
    if (!gl.getShaderParameter(s, gl.COMPILE_STATUS)) { gl.deleteShader(s); return null; }
    return s;
  }

  var vs = compile(gl.VERTEX_SHADER, VERT);
  var fs = compile(gl.FRAGMENT_SHADER, FRAG);
  if (!vs || !fs) return;

  var prog = gl.createProgram();
  gl.attachShader(prog, vs);
  gl.attachShader(prog, fs);
  gl.linkProgram(prog);
  if (!gl.getProgramParameter(prog, gl.LINK_STATUS)) return;
  gl.useProgram(prog);

  // Full-screen triangle.
  var vao = gl.createVertexArray();
  gl.bindVertexArray(vao);
  var buf = gl.createBuffer();
  gl.bindBuffer(gl.ARRAY_BUFFER, buf);
  gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1,-1, 3,-1, -1,3]), gl.STATIC_DRAW);
  var loc = gl.getAttribLocation(prog, 'position');
  gl.enableVertexAttribArray(loc);
  gl.vertexAttribPointer(loc, 2, gl.FLOAT, false, 0, 0);

  function u(name) { return gl.getUniformLocation(prog, name); }
  function hex(h) {
    var m = /^#?([a-f\d]{2})([a-f\d]{2})([a-f\d]{2})$/i.exec(h);
    return m ? [parseInt(m[1],16)/255, parseInt(m[2],16)/255, parseInt(m[3],16)/255] : [1,1,1];
  }

  // Static configuration, matching the component defaults.
  gl.uniform1f(u('uColumns'), 14.0);
  gl.uniform1f(u('uRows'), 8.0);
  gl.uniform1f(u('uThickness'), 0.1);
  gl.uniform1f(u('uSpeed'), 0.35);
  gl.uniform1f(u('uTravel'), 0.7);
  gl.uniform1f(u('uWaveSpread'), 0.9);
  gl.uniform1f(u('uRowOffset'), 1.0);
  gl.uniform1f(u('uSoftness'), 0.05);
  gl.uniform1f(u('uGlow'), 0.0);
  gl.uniform1f(u('uBrightness'), 1.0);
  gl.uniform1f(u('uContrast'), 1.0);
  gl.uniform1f(u('uOpacity'), 0.42);
  gl.uniform1f(u('uVertical'), 0.0);
  gl.uniform1f(u('uAlternate'), 0.0);
  gl.uniform1f(u('uMouseStrength'), 1.0);
  gl.uniform1f(u('uMouseRadius'), 0.3);
  gl.uniform1f(u('uEnableMouse'), 1.0);
  gl.uniform1f(u('uGrain'), 1.0);
  gl.uniform1f(u('uGrainIntensity'), 0.05);
  var c1 = hex('#A78BFA'), c2 = hex('#5227FF'), c3 = hex('#B497CF');
  gl.uniform3f(u('uColor1'), c1[0], c1[1], c1[2]);
  gl.uniform3f(u('uColor2'), c2[0], c2[1], c2[2]);
  gl.uniform3f(u('uColor3'), c3[0], c3[1], c3[2]);

  var uRes = u('iResolution'), uTime = u('iTime');
  var uMouse = u('uMouse'), uActive = u('uMouseActive');

  gl.clearColor(0, 0, 0, 0);
  gl.enable(gl.BLEND);
  gl.blendFunc(gl.ONE, gl.ONE_MINUS_SRC_ALPHA);

  function resize() {
    var dpr = Math.min(window.devicePixelRatio || 1, 2);
    var w = Math.max(1, Math.floor(window.innerWidth * dpr));
    var h = Math.max(1, Math.floor(window.innerHeight * dpr));
    if (canvas.width !== w || canvas.height !== h) {
      canvas.width = w; canvas.height = h;
      gl.viewport(0, 0, w, h);
      gl.uniform2f(uRes, w, h);
    }
  }
  resize();
  window.addEventListener('resize', resize);

  var mx = 0.5, my = 0.5, tx = 0.5, ty = 0.5, act = 0, tact = 0;
  window.addEventListener('mousemove', function (e) {
    tx = e.clientX / window.innerWidth;
    ty = 1.0 - e.clientY / window.innerHeight;
    tact = 1;
  }, { passive: true });
  document.addEventListener('mouseleave', function () { tact = 0; });

  var raf = 0, t0 = performance.now();
  function frame(t) {
    gl.uniform1f(uTime, (t - t0) * 0.001);
    mx += 0.05 * (tx - mx); my += 0.05 * (ty - my);
    act += 0.05 * (tact - act);
    gl.uniform2f(uMouse, mx, my);
    gl.uniform1f(uActive, act);
    gl.clear(gl.COLOR_BUFFER_BIT);
    gl.drawArrays(gl.TRIANGLES, 0, 3);
    raf = requestAnimationFrame(frame);
  }
  function start() { if (!raf) { t0 = performance.now(); raf = requestAnimationFrame(frame); } }
  function stop() { if (raf) { cancelAnimationFrame(raf); raf = 0; } }
  document.addEventListener('visibilitychange', function () {
    document.hidden ? stop() : start();
  });
  start();
})();
</script>`

// authLogoMark is the OctoPort mark used on every browser auth page — the same
// brand artwork the website and the desktop app ship. It is the dark-background
// PNG variant because the auth pages are always dark.
const authLogoMark = `<img class="mark" alt="" src="data:image/png;base64,` + authLogoMarkPNG + `">`

// authLogoMarkPNG is the 96x96 brand mark (white + periwinkle), base64-encoded,
// matching logo.rs in the desktop client.
const authLogoMarkPNG = `
iVBORw0KGgoAAAANSUhEUgAAAGAAAABgCAYAAADimHc4AAA7L0lEQVR42u19eVxUdff/+dw7G8Mu
IqsrKoaWmZZLKVpa5lO2PA+0+JSVpmWPZZtZ2TNgqeSuiIYioAjisO+L4IDmDooIiKzKvs8As8+9
9/z+mBkdScvKvt/6/vq8XrwYmLt87jmfcz5neZ9zAf4ef4+/x19oICKRItKIyLP8kclkPESk/qbQ
H0h4RKTv13F/lsH7ixCfJoSwAMAuXLjQ9vPPP5/qOWzYQwIBz4NjOLFAIOju7lbcaG5uOEsIuQIA
LCISACCEEO7v5fsbh1QqpU2EhP3797tU1dSs71H01BkYBu801Gq1vrOzs+BKefkrlswzX+Pv8RvV
TVVV1RK5Qt4ykOAsh8iw3B2Z0dHZmVNcXDzFkhF/U/aXB5HJZDfV4oULFx7v6uo6YUlYlUrd19HR
dfjy5bK3mpqaZigUiinFl8ufunrt2qddXV2n9Qb9zWM1Wq2hqaVlR3BwsBMAAEVRIJVK/2bE3dQN
IcT0Oc2jsbF5r0qlZs3E1Go12NTUdDgxI2Psz13n/Pni57u6ui5bMq23t+9GaWnZcgCg/lZLA4ZE
IqEs1AOvpqbuo74+ZbslAds7ui6cPHlynqU6kclkPCkibdonzGYpAQCYPHmyuKqqZnVff3/Pbddp
bz9x6tSpWRbX4QEA+VvdAEBxcfFTPXLF2dtXbm/b1aqqjwCAbya8RCKhfslispCqkc0tLRFqtfrm
RqHRaNjm5uYf9u3b5wlGM+n/lloyrWgCYP75eXWTlJQ0oqOjI1Kn090kvFKpNNRfv/5DVFTUbyXS
bZv46dOnfeUKRaElc5VKZVtlZeUnACD4BbV081lMxsFfS2IIIWAmtulBKQCA+fPnC2vr6z/t7+/r
siRMW1t7gUwmm2Y+3iQl5DZPWCqlJTIZTyYzesASmYwnld6UDnIX9QZXq66+19fX12R5v66ujgvn
zp17xvL6lp/JX0lDbd261eqjLzaOeOqpF528Z3jbgidY/VQyAE6dOveMorf30u3qRlF7+fLlxXez
3yUSpO7FlCSEgCksQe7kT0RERLi2NLXsUqvVGvO9dTodNjU1HY6JSXYfyAQAIOAJVj4+PjbPP7/E
ZdVX37nd1wV6nyx3AkBQItk6aPTohUW0jZ0NwzA6IcXTC4Ssslfe3FF8Pvf14OCvO8+dOzf/oYce
ShOJRDwAALVarenq6t61LXxr0M7AnYqBHqxEIqECAgLA/Lef31z7WU996ENRjj48WjyUQ6Q0Ol0f
Y+ivV8lvlAcGLq+8RXik/f0JewePGs6ePTvRa7TXhsFOgxeYv+/u6i6PjkmZ8+GHS7q2bJfO8Rzx
yBaWE1GcHuxBwIJY4GDf3VUetfydGR8NvPb/ciiCoOnhesIOPps7zHXI8r4+BAEQsBID3GisWBUc
/HWnVCqlxTZ200QiEQ+Rg9a29tTLJSUBCxYsuDSAQGheuf7+/mxgYCDs2Zk7zWbIqHfENuL5PJ5w
qNDKARApYDjj0Qa9DjiDQidNeuaKTq1JKr6QEOHvT1rNq5kQgoQQFhFJQUEBPW3atMsA8I+yiorX
PN3dJfb29t5WYsHY4cPdPAGw5/NPyPH9MVWqYUM9nlD1AVBCAIozQG9XV6zxmeP+XLGguDjjhBrb
T+1xGjz8HSBCSsMYGA4I5e4xau7XX4em+/v71wLAtxWVlQqC2PHAAw/EWFgunHl1AgBIZDKe/5w5
zEcfbRwxceqCjTZiT3+x9SCKQwD65q6AQCMCUBTwrYUAnIsQEKbYOcIUX4dlqyZN/sdmQshmsyQF
BgZyhBAEAMa8FxFCjvj5+aVt2bLlo4a6ulMvvmhcDKGhJ150cLBz0ahYPcsaOD4lEsjlzSdXr154
BhEpy7n+CVSQcbX6+flRhBBDRFRtpqv7qPlIAeFYAIEQoL+vu62xvuLfq/4zK3/AhveTgJlEIuMF
Bs5hdv8gm+/qMf4gX+w0RKcjIKIJ9Cua1SxqflSp+i5TNK+Z5lgDEoOV0Eo8Wiy2m07zRRNFImfQ
6QCEAgIdrdXZx3NDXjt4cKfCzIS7BPrMgwo/dGm9m5vPGoYIQMADYDgWgAOsqT7xxofvzYmprq4W
jBkzRm9i5p/KkQJCCGzdmT4/53gn7o28tDE2qbYmLQ8xIZPBhPRWw+ZtOYsBAMrKygR3YyQAQEj4
yZfT87r1KcdYTMtHPJrS1rP/4MVvPvss1OtnpkNt25XyeLS0KjYurQ/j0jRsei7i4aPXileuXO9M
CIE7+RGmZxD4+PgIwg9fOJJ5HDEunTFkH+cwMqYiNiapOicxrbF78eLForv5HP8rw9KRulhaOre6
ujYcAKhDhw5Zh0emLgcAWLz4M9eImMrstDwGk3P0hrQcJe7Zc/JZS2IPNFHXb4p/PDFDrk/JY5ic
Qg7jUpuOr10XPsbyOKMJajRDZTLkSWQyHliYu9t25/1LmtzcnZjJchnHEA8drToxejQIpVKkB/on
xv8BHDpy+UDeKcS4HNTFZ/bgvrCzXwEAbN8eMSLsYO4bAADnT1189MaNG7skEsmQ/zVHztIhSU09
Nqy9s3OfRqthEREvXbr04sAHAwBBdPzVqNyTiGn5LCtNamqTSHa7IiK5tSKRICLlt2yZ/cGY6trE
bA6T87QYdbT8sNkTlsmQ90uesFQqpWUy5AEArN24a2JsSm1TcjYyeacQw6JObRgwr5ufd4fmv5N1
nMGkbNQn5ymYHfszF9/JJG3t6Dhs8tJvVNXUvGcZX/ofUznmzxXV1Z/0q1S3OVK1dde3m8wivp+f
H/3CC4sdzJM/LL2am5xjwIzjiJHRpWGWBDD//mFfyfdpeYhJuRwejK2UAQBldr5+zTxDi4r4AADf
bY2alnysW5uej0xSWpchaKv0QSPjkQJEAkBgzYZgp+ikxo7ELGRTslS4Z/+J983E9/Pzs5JIJJRU
KqVXrFhh09zeXntb2Luj81R+fv4sSwn+w4m/IXiDU1tba6blRBRyRVNpadlKSWio2GJlk2++CZon
kez0AQB4//0tQ+OSWrrTjyGXkNapkUh2DLNURV99t9cjLq25PzkH2ZjE1r61a+PH/B4RLzIxYU9E
4TcpxxjMyEOMjiqPNjPcrEJ3Hyj4NPsEYoYM8UBMSZL5/HUb9i5YtUYyyvzsEomEOiiVDqtvbApT
azQ340tqjZqtrKpc/YdKgtmTDQ4Oduru6b5ozIiwnEqlZhoamnZs2rTJ9U4W1oYNB52C92QdDgqS
2gMAHDhUvCO3ADE9l8GQ0OMrjN5pvQgAIPJw8cc5JxnMKUSMjL68xax2fo+DKJEgNXnyc+KYhIbr
GQXIxSW2dn/55Q4XS6k7eKTseHo+ckmZCnb9tvjHEZFs3x47K3hvwkaTuv3Jyj5x+rRvd3e3Kb7E
GBARq2vr1/1aJtyzyIwfP54QQshLL78cNchx0CSOZbFfpWqtrb3+1LBhnqtWr17dNuDGiIjUV18t
7nYaPHSci5vnm4hIWEaZpVRrgFA0CAWDHgcAeOutEQwAgEjk9AJFaFT2d2rb2ssjjE4T/I6cLkGY
XUAVF6ertaq2QyyDhBIPGuQ5auI8AAB/f4p9++23na1t7B/k0UDUSvn1gqzQEkIIDnb2+VIkctEQ
QrCgoIAamK2bNWNGoZOTk+/1hoa1BgPHA+AMo0eN+OZyWcVrJoePvm8MQETa39+fvXTp0pse7u7P
AgCr0qo7jh8//uRDD/kUmmPxd3BOEABAILLhC62tpxJCsE/Ze0OrUWopGoCmbR2MmSqamTx5spjw
rEYABUSlkl9d88nrFRRFYWDg70uqj+/sRAAgXW1VOZr+XhRZCdDKxvlB8/TcRz1gR4jIiqYBCM3d
OHbsmAoAKAPwH9GoRHrjJjzbMtaEhBDWFF+iRg4fvr6qpuY/LIt8AMDhnh47ExMTnQCA+yWD4Z4Y
YLICOF9fX9GwYcO+BADOYDDQl0vL3n3xxRevlZWVCQghzECnxBjDAbJopcROZGU32sChHADAyspB
wBfQNMcBcBxrugcHb731tSfHoSvDAhgYQz0QghzH/e5Nrby8HAEAm5qqG4FR91OAhKZ5483fq1Qs
siyigQFgb90PBUKBwdHecerdnC1/f3+WEIKIyJ/g4xPS0tZ+GACIvb2d84MPPvgeIQQDAgKo+yEB
FCEEd+3a9bi9nd1YAKC6u3tyZs6YkYaIvAkTJujvdJK7+/N0YCDhZjy48EsroYNYIW9KREQiFIq9
xXY2fEIDckTbdYvRdi58ylrIsgAGLdMCiFBQAL+bAYGBAQgAkJcX12MgXA+PJmjFE7ubt6lLV8/2
6NVqJcsCANLeL77xgRMAIA91qcNHDX3xu+9iH5kzhzB3MQQQjBAY6tSF89+o1Ro1AKDTYKdXQkND
+QDAWobifysDCACAk5PTczSPhwAIvb0Kk34uuKstvnz5FMN3W9KeGTHygTW1VVeyP1r+XCEhBGmK
9w+K4gEBLdHr+29egGUNNAABggCAYLiXsDP+CuvIysqKppB1oDgD0WgUbUZdzvELU1IUOkP/RZoA
2jsMcZ094+WpiEhKLp7Z0qfoV472mRUz12+ZvZ+f3x1VijmM8tpLL13vV/bnAwCxshI/OHTUqIcJ
IXj06FH69zKAM+ppajQAEKVSpSoqKjpr2py4n1pLSL3yyivsGsn+0Q9OnCZV6/qar9blLSWEcGvX
7h4vFnu+olNSqO7rUTTVXcg0nye0pbqQ0jMEAChCuwAQ6JwN+DPhVyD+/ubNjvzsRgwAzz33HNsj
L3n3SmnezFblmddMutwAAGDgFCkCHkcEAmtwcR21mhCCQUFL6qquFS9xcHLzfv25NWEmlULuci8K
EUlPd3cBAIBIKIQRwzy8AACcnZ1/X7zNbIL19vcXICL29fU2+fn52d/BSwSAmw4TLyG54cf8H9W4
flvSTNOXdGRMqSwpGzHzGOKBA8WbjFFHo63u6+dnk5De3JB7CvFgTP25O2WnTEkaASEErl2rfvfq
1atvmb+TmOZ5r07bZLfJ4iNHjozdfOiQ9aeffmqdmFpXl5aHXFquAXfvMTpiAAAh+4rXnStCjIi6
sHKgFz0wJFNaWrroZmavo22pReL/95uhFDG73AA6nY67s+oByt/fn90SUrjK1XXo41XlFz/5+pOX
TgIA7D94bquL54TZSAB75Ncby8ouBiEiaWlJYxGRKoyLUxoM6joAQL7I2mfdtpjxpk2OMkUskRDC
EkL0iAi2ttYjxo0bF1FbW7+OEAKBhHAmSwz9jZJhyQjq9PmLvleuVK6oqWnc0tLWnC67VlAxd97c
06SjY9DWrVtVXZ11n3Kskhi0PNbdw2frd1ujpgEAfLBsckBjQ0OBp+eErRu2Hhrt5wecRHJnj1cg
EDDmz3y+8P5Ems0S0N+v/NGEsWlauHCh7Z1WKADAsmWSwdKk1v6DR2rPmsV1R3Dmq1nHDZhyjNGn
5vZzW7ZnPWPpAZtXUETMpVW5JxFzZAYM2Z/3bzPxAQAWLlxoW1lZ/UJrW9tBuVxeotFoe1jWiFHs
7unJPXny5EREpGJi4iYVFhY+ZjEvvp+fH11ZVSMZiJ5TKpVXLRP4oWGn92TmIabkMlxCWmO9RBLh
SgiBb78+4pWeLdfvO1h+5E5SYJ5/RcW1V83X7u6Wv3tfJeCXRkEB0AAAj8549o3Bjs421+tKP0dE
+Prbw8MHe/iEqA3IWQmA39Nx9evPPn42RyaT8fz9/Vnjuca9pOLSj1JFV11/a3Ppuo6mk1JCCEcI
ITU1dR9FRUVd8vb2SnZ1cXnTwcFhokgkdOQ44xYxyNFxnr29/UxCCKfR9PWOGzcuTS6X55aWlvoS
QgyJiYnsuLGjA9s7ujIAgOE46Oc4juvv77tosmQIIlKbg978WK64WiTgE2Jv7zli1LgZexGR+mb9
a7WNLVUh9nYuryxfvn2Evz9h72zjs3Dfx0AJ6Lu7BFAAAPFJrRfikmrLzf//IaLoh7R8xIRcA4Yd
Kkm0cNXvKKJfrN36oPnzsYLTD7Z3dF24DZqoVhra29uK5ArFNUREhULRWFhY+LQZ4tLW1lppmWy/
cePGLl9fX5va2vqvDAZGy7KsGW3H1tRcW2tepUa1QmCNZP/opMzGntQcZOMzFfjd9vhnJBIJtUay
Z1RccjfuDy9dPjAUb/5cWVnpb763XH5vEvCbVND8+fPtbmeA8bfvcF9RXFqTMkxavBGAwLebw4Yf
TW3uS8lGLjquSf7WW18PvT0Effdx+fLFZ5UqtcJCXcirq2s3FRQUPAoAVHV1/bfd3fLzaXl5HgAA
VVUoPH369KCampqXW9vaEixxRl3tHZVatdaELVUyDU034hERa2pqXrUkoPl3yL7c91JzdJici1xE
VHmq2ZySJjRVxCVWHRkY77kTAzq7O5ffZxWEPxOoCyAAANNfe9Ndp9dYcwYsAECwtfWaaWPnZisS
AdGpFUciI9c3FhQU0APTggMzTOfOnXpm7FjvZGuxlR0AQEdHR2Z+fv6jY8Z4rZ49e/YFAOAuXIjZ
7eTk+Pjzc+c2AwCMHUt0M2bM6Bk9enSim6vrPytral5UqhQVHMsyTkOcvYVWfFSp1VxVVfW7wzyH
/6uktGR1c3NHHQDA7NmzOQCAOXNmG5P2x/YfVHR1NtAUEJ7I6tG3317pDACoN8jPGFjmgbsRhGXZ
m1JN0dQ9pSt/g3jc6boBABAIYoehQlbDMc3X67sBAMQiuzGIBA3IEh0jP/tzzhsiUhRFscnJyd7j
x0+MFYnEPAAkdddvbPIaOfKLm8zetNt1mMeUhWqD9bSoo//2IjyeLY+PXRqlokKhaMn6eMXTxwCA
mzh+fEpVVeVcrzEODwBwjFarw8qqqkVTJk2KQ0SeOVlv6UwZfQak4uLiNPOf23gZKBgmFNoMnjTp
afeIiOBOjU7XaWVtTf/iivwV28E9M+DnXOqAAMDAQAC1QtnBG2no4VitEbPAEiEyQHR6HWgZXZ8x
to/4c+pu6vTp+62trR0AAOpvNGz3GjnyC5N5Se0PL1o92G3Eh0JrJ1ceH4BCowyzFICdwWOenf3I
j+JT6k7eqCn5xv/Vqc97urv/BwBQq9Uy9fV1flMmTUoHAD4hxGBCNnA/RXcAQUQSeaRCSxCAYWgi
FPJoI3tYQjhDp+k46ufITNM0/EESACAQCHAAc9BEpO6YuKIbbm4egwAAdBplO8swyOcLCY+IhhJC
UC4v/snEzciEq1erXncdMmQmAEBHd9vJUSNGfIqI1PTpQ4URhyujnd28X+JoAD2jBVVvb5tBpb2m
VevVfFveEIpnPd5h0BCRgB45cwzPoYBBY85frVGqq67VvDxp0qTcoqIiL1dXj70lJcVvEkLazH6D
5Vz8/AAJIRgdX+PFowAYnUZZUVHSAwBEr1UL+Xz6vNHDLSA/LwDs/WFAXFyc5Y04iqK4n9lPWGWv
4oyVyNobAHLUbPc5DvuJiO+I7i7DFiFi8LJlk5mWFiPsZEC4gzg7D/ocAFCt0WhPnzz7gcn5IoeO
lBwa4uH9kkoNoJY3XO+X1wbUXC3M2LYt8GYwb/36Az6D3Ca+5+w84T2KdiTVVRoey3R3KxTX/KdM
evx4dnb26FGjRmU5OjqO7tf6vAIAOwGABgDm5iYrRT4hRL9tV87j9jauE4ECjuLrq3fu/LIBAJDm
4ZDu9pZIAIA9ezrxDqseb4Zu0Ein4uJi8psYYLZwKIpiAQC0Wo3I2tqa0mq1wq6uO6khY9SxTyWP
FBq07wIAaHpKigzaCUViseMUKxu3qVGxJbsJIavMwKiAgABzooc9dercLDs7+4cAgHT39CS89NJL
VwAAQiNPLHcbNvFfOh0LelX1+bOy2JdCQgJbLOZIAQASQio2bJHJFX3VMPYBH16/Utsce7hw4dq1
/7yIiFRcXBzLFwhcAYB1drR709fXN8QkiUQqRcrfn2L9/Yn+kzVbxroPHXmAQyvCJ0BpNT1SAOCC
diZ4Auihvr6+8C65D2A4hjYbNjqNjgcAMHnyZO7nYIy8O4cUboGVPv/8e9vr10eqr1wpe8/Gxt5R
ruztnzDBVTtvHlL+/nHEz08KPj5+GBgIiBhACCGXNm794bRkw9bRgV99WhMSOvUzK1tHmYF14mzs
H/jg0JGaRzvaqv9LCMkx5m2RDwCsq7vrPD6fR+kNemxoagpHRLJ8+XI7a7HLlywCp9d0ttUWZ70Y
EhLYGhpaxF++fDITEBBAAgICOEII7D9Uus/V3efdjo4uuF51rlrLdr+8du0/yx566CFrAND5+/vX
32hsjLWxtl5qJRRO+Pjjj70IIdfMKDdfXxC9/V7JfwRCzy8pvoMj4VGkpeXqpYtnvt8LAMBn5NMZ
nWZ/cPBHupkz3WiJRIIAAVBREUc6O2ejRIJURV2GTKXWzucMHNXbqymTSGS8Z58dQ2dn1+gQkQQE
APnZBJMJgUwAAP793qdDDktL40LDClbcO04IeYhIrVy5Uvj55xJ387W27D72vjS1m0vKQkzKRkzK
6sOYpMqEzTvSJpj8DNLZ0ZmLiNjb19f6wZo1TgAAO/Zk+SVnajDjGIe7Q2UfWwbv/PyktMmXEwTv
v3hUmqbHhCw1HoqpvrZ8sWQEAMCGTYnTIw5dKFyzcY8jIpKysrLXzXZ6ZXnlElM4RLB5Z96/4pOb
SrNlWkzKYTCrAPFQbE3tZ1/v9AIA8PWV8Fau/NTHjNr7NTUC4VElSfsOXnhvgKlN7iQBJDCQcIGB
AHsPHF/iPMRb4uLqPrS87FrbN4FRj9paDRFwnBo5ikZOT1gALWcAA1CUkHA6na6urrR5zhzSbbqJ
nhDSsnlzoAlgO2/vxs2x5V5jpm5xtB/xqEovgiFu3i9r+xghIeQ5X19fG4qivAAA9Hr9pZCgoG4A
AMdBnrNs7EXQ2dzeV3etON704Eyeo5SO8/dn/fxW2MxdsCLewXHMMwxrAHn39bIfT0v/EXUwsGHH
jrQJnmOmpQiE9s42LVVjCCHnMzMzz3l5ealEIpE1zaMnAADUtyiHD3ObGifkewCjAaCgE7o7Ww/9
ePzomn37NrSa4IxMYSFUmFQJExgI8OGHkmFDhowZRAkH8cHAAMdxyFIsEfOFFEVRxAAG0DAKxsra
c9IQF6cXjyRXPl9bVfQ1IaTkJyrIqNMAdoVmzHV3n7yWL3DyReRDa6uOHeTk/h97hwUfIEFAjiXI
AhCkkTNBs3gUBXyaYyc++mTn0/94O6um7kQQIaRGJkPenDmE8ff3Z00TPwEAT+w/cOIDW6fhn4rt
hnkgcv0AAP9evtya4lE2AAAsw7XfjCgK+KP4IgCE/oZt2z5r2bbtM5TIZLw4f39m2aqv3KZPfSvZ
3mHUYxzHgryz9mx+xqGX4uK2tEkkEorwiJeVjcgZOJpzGuQ5BgDONzc3KxmWUQOAtcjKaOqq5X06
oZdIzbMCcXdb7emunrr/rvrP0/nmtGpgYKApERMA/v6E3bQt6wX3oQ98IBQKp3LIE7McS7OIQJBG
QAKEAKEoDghFAIAGHhFjd5eWs7b2XjB27JDZ0bEVYVU3Tn8fsHpJ600GBAAQAMJZiwumCsV2vgzw
QdvPMARYWiy2Bb4QCGsEIQNwxoMJbcx00EYbhtIz4C6wdlniLbB7aUdw7ntz5pA4I+GB8/cnrJ9U
Sse/8or+3SWzti9eLDny1LP/3KrVdSAAgKvtEB6Pz+MBAHCc4aZ1wTB6Hg3A0RShAXyJFAtof0KY
5cslIx6d9GqGja2XD1IA/Yp6WXLs/hezs4P7JBKpIDDQX//tt5FyZZ8KHe1tKAAQAQDodLybZidF
EZ7RuBBSKmVLW7+qMXjx61N2AgBKpUj7+QFHCOH8/KR0YKA/CxDICwsv2evk4r2MCETAowD4PFMQ
xliXQzgOgIcmGxuNW7xayxFGz7BqNcM5ODiK++TyF2xpfiQh0CqRBBgnYRFLX7826HDqmNGPbBrs
OG4+TcTQ3Fwn6+tvuSAQCggh1kiQ5hHgdCxotBQFYmB4YGUl8hSIHJ4W2Tg58wUug0aMsZVGRJ/5
r78/+da4+UioQGPkk0hkMjpwzpy2gwcDFy1dutoTACAzM0H9+BOTtCAGAIoTmxmg09EtBIFCICpf
3xE8f0K0q9eGPDD+gblpIqvhXsgZoKO5LCMsZLl/cXGx2rgvOHOISEJCCl2F/EFEpdYAgr4dAGDI
EGuapnkCAACdVtcHAOA4ykmekRIxIzZ2VzshBI4ePXrTYjGjtN/79NMhc2Ytj3a0HzO3Twkc0amp
3v6Wywad/keDgVUSiuVYimhpHs+WzwEYGBZYxkAMnJZ2HDx+kbu7g7NS0amqq6nfumLp1A0AoAN4
k/wkJGOZSfoh8sTbqZldPT/sK1p1p81la1Dog5Z/f/zxdx7hhyuisgo5TDrG6HNPMHgoqmwvANAD
0W2WYCeTh83r7Oq8YowidpROnryMDwAQFlm8NjmtsWHLltRxAAAbd8ZNik683hSXiRifqcU9Yeel
ACCwRD2bgVzR0oqI/FOI0UnX+yUb94wyBvguP8UwDGsEUdV+aRlsNMX4iaVBYbQCd4yRptaXZ5xE
TM5FLjGjW7V3n+xdM14VAEDqB/S6tUHj70SnQ0cbrh5Jun5mXVDwg3eCd/6sJbRmzZaxO0ISHg0N
LeKbQxHBe44/k5TWVXdEWplxp/MjYy4EZuXrMCkT9enHWIw4WJH62GOP2ZkfSiqV0hIJUlKplA4N
LeIjIh8AoLGxORIRObVapdm37+BYAICN2yNGbNiwezQAwIcf7n5iS/C5ttRjiIk5OgyPvRgKAIRQ
lAXxZSai7R2TlN3WV3AWuZiEK5lmwlZUVUoQEQ16A549e/ZJI0weBaGhRXxjfYOUNlk5tBFRcWTm
EWljS1IWYsoxDuMzG9o2bk+aPTA0I5EEeR6RVnERh+sjP/n626HmZy0qQn5oZPacW+Bi2U/qksnd
Uca3Ow+STemuY0f5rLe2GfwOzbMFeU97q0HTd04kZOsVvbXxHyx/7jQhFCByEBJy4u3BbuNDidCB
J+ARolffKGqqL3rrk0/8yu8WaiopufLOxIkTwgAA6+oavvHyGr7eHKLYsSN/wVCvh6S1DT3WfMKq
7ex0O5b8e9LXoUXILy7eB24tYzEgYDYSQlg/v2lWC18+nG8/2Gs6n9JD4/UTzy97Z24GEEK3d3We
HeI0+JGenp7uXbt2eQcGruu5W0wtLOLHtwYN9gnhKDsrPk0TtbKurL4q859ffbWyyhiK8LHZEBT1
otjabq5WDyNFtm4z7RytiUHZ3tLd3rD23UOfR2FBAWvec+5UHALwCxUyRrXhBxfKA1weHv+v08NG
Thje3K41IMPQIp4NZWVj9Ls5vRr6eq/H5meHfXDgwI4eAITvvst6ZuyESVFiGxdnRACDTq7q6W6M
UPZ15HR3KFsFYrB1cXZ7UqfprFi58vnYhISEIfOfmV8qtha7NDY25h04cODZwMBA5ty5opf6VSOj
+wy2fAYJNNVeufLJB4/MBADVwPnu2ps90cVtbDBf6DnTiseHluaSqCVvTVoMAPjjmTMLpj06OYOm
+dDU1HJw6FCPt1auXCkcN/HFTQYdXSfv6b9McQZ0cHL2cnJ2/afD4KELGFbIWgmBbm2qKohPD/BL
PxLbBYCwcXPc7JFjHt1jazf0AZqmAFiAvj4EhqgMVgIb/hAngPKKvKBlb8370uw0mtEZv4oBZn29
b98+HvB8fG3svIIcnNwm63QsIqs30KDnrET2Ir0egAhZkPfUldVUF/8z8KvXqgAAvt8T5T3cc5bU
cdCwhzRqAJoG0KhVYDCoOCQU5e4yGLo6qi+G5wfPyA4O1jU0NOyxsbF5+tKlS0899dRTN85eOP3m
xAcfjujro0jFNR7RGmjQG1hUKJqadXp5oqa3p0jdr1K7uA0ZbCUe/CTfatDzYltHKxEF0NxclXM0
+tOX09LStIQQqqu7q8hpkNODOr0OLpwveXzmzGln94UXPjl09NR8rUYIWpUKCGFBKLQBHp8CDlkQ
CBno6miIDNn65orz5y9oOI6F4P2Fi12GeO8TiVwEFA+A0/cBx7IaDqz5fL6AZ2D6oL/3Rpi8v/Lb
j5b5NRo11d1LmX5V5t7Pz89mwUvfrR061POL/j75j/3dTW+yLO95kY3zar7NMA9CAIiuvaO1qejV
Dz54TgYA4OPra/P58q1rrUQuy4VWbg4CEQ08HgDHAhAWQKtuhpbG0unvv//subi4DBcl289/57XX
Gi+Vli33GTf6BwFfCAAcnDhRHdzWKXzcyXXEIwwa0RmcDkHHGIAvoEHEp4HjAAx6Faj7msPjji7+
j1R6RkcI4Zqabuz08Bj2IQDAjYam2BHDh74GAHD4yKWQoSMfXtGvAeDRxmuyegCDVgNqdVutXNGw
4T/LZ4ebVCs5FHMl0MHJ6xs9K2Bplqbl8poMQtq28PigdXN95IxSqarvklesWvrG7NR7DvPfa0XM
l18GOQQFfSk3ohwS5jkOtnlw8WvPbAMA+PCLoGGPTX7tyCDnYTMYgw4Jp2KaWi+9//7bcw9QFAUc
x8GKFZuHjx0/5UknJ5dHgaPdCUUUOo38ilrZI/Py8io/d26MISDAGAqurKz8YtSoUUF8Pp/VajXM
tWtVHzz88MMH3nlnoe2jT3y+2MHBY5GAL/YRCmztkCcGjbYPtGp5EzDqsx3t9fs+W/WPY+b7VlZW
fuPt7b0OAFAuV3TJZMcnDnr55c6CgABOrx/m5OI+bKrj4MFTaMIbywGfTzis7e5oObFv/coTFZ0V
SgIA02cstF2xalO4i4v3v7RaYAil4zU3XQlc/vajAQAAeyPTPMQ8u88vX7zw3bZtn3UBAEhDpfb+
y/1770dSniAiaW5vS6+urvsMBrQCMMdm5s2bZx2TUBab+yNiQi4akrPVuC/8zPf3Vjlye1V7bW1N
KCIii4xOre5TXbhw8V8Dz9i4ce+I4D1pz+zak/PsB6tCH1m2bJnbAGm1am5tDTGFfhiNTsVatiS4
R1AyrPh08/CDcdXnU48jJmWzXEpWFxMaftac76WMVt0ts3JvZKRHS0t7WltHxz0Bs+45Ka9Uqs8g
IjY0NsYuXiwRWdrNEglShFAAAFREbElQYrYG49ORSc9BjDhYETdjhhFFsXNnldCEbqMQkTb2eDBW
S65bd2DiunV7Jpnv29h0Y//NxlgcYltbx6Hk5GQfX1/fAW0IgK6tq9t78uRJRwCAadOmWV26dOml
ru7uklutzDRMdV25n/mczdv2PenjY2zSIZWWCUytDWhjGxzkZWZWCQEAtmxJfSI2sbEhKQsxIQsx
JrG1I2h72jMWfgIx0wgR6eLS0in9yv4GRES5UvnWfWWAQtF7AhGZvr7+nvmLFtndGZposkZC8pbE
J3doE9ORyzqGKE2qKl4j2TZqAJzjJiBq856cZ+NT23sjj1y/8fW3h0ZSlHFB1dXVfWcwGNASZlJa
VrbDfJ+sY8em98jlVYiIRUVF716vu/6dQqEoY9hb5ygUitaioqKnzc+yO+zcopScHjwQfSVr2Sef
DAYwFoWbHvbmnHbvO74oObNLk5zLYmouYtTR+rIvvwybcOcosPH8srKyt8wS1yXvW3KfYSmqk+bu
U5a4IDNMb+OmhJc2bUt8wXzelh1JT8Wlt7RlHUfMPo4Yn9ra/O2W6CfMOQDzdfeGnlmRnNHDJOci
c/ws4r5DBftNziAPAKCkpGR+b29vsQlrc+LkyZPDJBERohs3bnysVCn7EREZxqJP2a2+QExTS0t0
WFjYcDNCTrJpt2uMtKo/PQ8xLR8xNqGp7PO1e7wBAEKLkG+SYtgTeu6b9Ew9phxjDDknEaPjr+b4
GZERAADU1p3SVatXB3ma1acFLOUVRGT/EGQcd0tP03q93mLlG8t3HB2GuYzznpMcvOfHpQAAn616
Kf9qeaavWtVUggBA8V3dvb2fyNmxJ//1KVOIgRDChUeVbvMYPjnEwDoCRQx0c0NZjA4NawIDCUcI
YYqKivgPP/xwtr29/fTa2tr3q2trC4qKivrGW1sL+XzBS9ZiaxuOYw0AxpgWY2BAqVTe6OrpOVBW
Wenr6e6+aOnSpTckEomAEGII+PyD9q6u1he0yrZmzgBgJfYYP3XqC4W79xYsWD6FGLy8RgkPHCyN
8Bg5cZ2WpRkeMfA6Gq/uXfSvB56PiwjuXLhwoe2h6Gsxzi4Ttjcr6lhLSI4xkMhRt2hK7snAuXdc
0F3yOOPHz0YAAKWqv54BKxw1ZtL+gzEXgwCA/PeLpddS49fOaW0vS6X5AADDRMOHToveF3ZlQ/iR
S/HuHhM+BsJnhSIV3dxwKfAN/wcXrVw8r3vduoSJkTFnpGnFxXxTDEk/evToHzbX1QXqdGL09/fv
dXd3m1VfW7+BEJrPsgwpKyv7ovhi8ZNbtmyZ4uzktPTRSZNOGRFvEmrE2Cefj5KeCyWE4IcrfI8X
nU2Yre6vvyAQAtB8NxcX90kZP4Rf+CJgfVqiq+eDb6kZimOwh1dbVfTZG6/7rAAA/Zo120a99ta2
fI8RY1/hOOuaJt01uWUByE/IxbH3DZxLbkfG9d4GTTR///33Me7SlEYmLY/lsgsRo+IqpWYEHQBQ
+w6WbkvNZTE5G9ksIwAWU3IQkzO6VOHhJ18332/bzsyn41PaumLjGjp9fX1FAACbNx8aGRKS+MBA
y8xUgvRKS1tzs0QiGWSZ59i8/dDUzz//3hYAYF/UhX/JziAeiq2UvmGsgIH5i+bbRcfWxmbJOEzI
YNmMY4ip2Sym5iHGpLQpgnblvmy+WFBQ7DRpYktj2jHEY6cRY6SVqZYgXYs94LVbyLju+4aMI0aO
GvE8iECEQiFlCUmRSJD64ovXW/u7mlYi24dKNXB29t5+i97ZfezjNVtHAwC3bPFDn3S2Ff+HpuWo
Meg4jqFBp+6ob2g4Meedd2bGAACEHzn/0aixj2dY2bo46VhWZWtrSxmrc7zmeIyYXRwVV5O7LyJ3
GiEUxsXFUYhIjx8//mhOVu7jTz31FC2VSunwqIKP4lPbLrh6TM9o7G6wBQBQdHdjv4IFx8Hefi+8
8sWJHTsyH8qOzu5b9KrXq52dJQE0paRUSo4hPAoM2qaKumtZs9d8+HSi0ec59vqIcY/nUYLBnhQB
6O9pruzoKv8KEUl5+d3BWQgsd19VkEqtMsdebKZNe9Lm9joswkkkErJkyfS9ZVdkL+h1LXKW5cDW
1uuxx6e+Xrh9d84sAIAlbz4WUlld8Dyr7+nvlVdfSMnYOuvD918+DwD03vCiXW5uj+4AnjXhCxAM
enl2enq61pQXYHhCRyuPYV7z+EL7RwEQnJ2dCSGElSLSb7/99vVZs2Z1+vv7s3b2nq+5uLs8zKE9
6uQKNBaX3CjRaBoaaR6AldVQn5FjphRs3Zk8BwDgTf9HAmtrTvsDtDJdHVdPJqXvmLN29dslAAD7
Dp1dN2L01Gia5yriC2no6io/Jj0cMnvVin+WmZ/bBG0ERCQsyzqaaaJQKDQm5PfvL9IzbnCGOgBA
W1sbuwUL5k42Nz66xYRATiKT8b767OX0iqLkmTpV7RWhCMDKysV99JjHckMjzr4BAPD5ipezis8k
PCbLXf90dNimJokEqV17Ty0fOvyRlSo1y7JMH32j9se1S9985D2ZyVLiCcQ0TQBV/RzLoUBnOTl/
YoSKm/MNrF6gVKs5juZpQCgUcQAA679ZXnvxcuEspeLaaYIcEIGz4xC3h+K++CJ0GCLSn658Nq70
SvKU5KPfLzz8w9aOyZMniw/GlscMHfHoN8ATMTZimu5oLt+3+NUJz8bGbmyXGJEUeJsnSQgOGTJk
uhEjykDj9cYGAIDOzk78vXsAbdK1L3CcsTq/ubVDejf9Zrap16zZ6ChNrEzPLWQxORcNSblKDA0v
XjfQxPXz86MPH60tS8tDNjq5XbVpR86/zd+ZKg0h7GDJO9kyxLTjLEbEFL83EB5uaS7HHL0hyy1E
PBLf3L1okdE7lkhuHmsVGV0plaYa2PQCxOCwws/Mzpj5Om8skwyLjK04mS5DTMpjmZScPgyLKvr8
ltl9ezLF1EGAOph40EmlUnUjItfb29sQFhZma8oZkN9dpEcIgcjIyON9vb0tAMA5OTq8kCuTTSOE
MAOZEDhnDiOVSumgoC/l/i+PW9hw/XIIQYan11kx7u6PfHMo+lrssmVB9ibsEefjs9BaZCV2EoiA
0mv6yleveuawCauDLWPHGvcdRo+IAAQo4MjPQ0KQRuAQwLLbYUDAbHaZkZmaK1eKv6dAS/F5gK5D
PD0sz92yM3vG809/cNLewfsJrQpA19etunb19L+WvjFlszE3YZT0AYA0HiGEe/yhJ9aIxeJBAEC6
u3tSli5d2s9xHP1LIN5fZAAxFkzTmzdv7m9ta9sOAJRQyOdPmfhw5Pbt2x0smHDzif39/VnTysCl
ix/5T0P9jx/yQE6xCJzHiLGv+D7zcnxcHAFAJApFF8UwNKEJgIBPTChp2tgJruDm6tYzDABNACgk
JobPvjMoluIBEgCGGDg9T3fz4eV5jhwAEufBYiHNY4AGIKxezzdupuOZ/ftPPDTO65FsWzvnYXwh
gFbTVFtalDNn9ar5CTIZ8oxAtVtqx/R8fEKI/uLF0gUeHu6rAIBVqdTayvqrO0xWGt6XTZgYk/Z0
UFDQ7rbOzjMAQBwdHbzffPPNrMTERC9zpbxl3tfUnw0QkV753pzgltaKF5D09PapdAZ7h9FzZ839
8W0gBJ3dhowFInDS6wAJMfQSQjiOYykziRGR0jCMNYsAjJ4F1DMakwN4x7kadEodwwIwDLEa5jlB
aOq6SHx8/BCAIIs9Cgr1rF4HqGOZKYQQXLeO5gRiz+18kbMti3pUyq8XFF06OHPDhjcuSmQy3pw5
xBLHSmQyGc/0fIby8kr/cePGSEUiEQIA3djYsHbBUwtqwVjgfv/eXWBSC5Cenz+8r98YcDJ1M++u
rq69rQPtQD1pgh/C5p2ZzyekadiMPOTCDl8JBgDhkbj6wrRc5FJy9Lg1JPvNWzobiVm9hR8pS0o9
jhib0I7ffit9YiCAwHKv2ht2endaLnIJGWrcuiftVUsdL5EgBX5AR0SVnsrMQ0zJUeIPB86uAgCI
jm/uSMtDLjq2sdIMY7nbPQAApCkpo5uamqMYY6MUBhGxsbEx8pdKsH5PfQBnYsKNH8+fn+czblyK
o62tt1hsNWj06FFbu3t6Xq9vaPjajPk0EY8lhGBaGrBSqZQuL2++qndTcmJrEQ9Q9PD+iNJjYpvh
M3l8gPa2qsKY8K+PSCRIVVTEISE0EsIxByLPvD/EacQ/CAGOA1VDRkZcsQlCww1AcQMAgEbfm0xA
/YFIIGZHDp/8/bp1+y/7+0+4erOyMRDYzumtq20cPQppahDlNHj89t1hJQ8JhGK0EgLp0ikbAEBr
2czPAsHBPvfcc+JNmzZ95OHpudrO1thsBADoppaWiKFDhy41Hcvdi/r5TcO8KjYEBzvV1deHqVSq
m42L9Ho9tre3R+Xm5lr2d6PNATvJ+kMTYpNa2PgMHZeQwWFiBmJGPuLh+Lqyr74KdTPCS6QCAAA3
NxCHRZ/bnZ6nwuQsNOSf5PCHA6fevlOZ6ABLiMQmlqcUnkFMy9Vxadltndt25/qbrRiz9bQ1JP/N
hLRuLjUTMSUXMT6r35AhY7nI6Kp8c/s0y+MBAEpLy1/s6ZGXWQb95IrelitXriwZ6KH/ocMywZJf
WDirs6uz4PaXLKgUdXXX/2tCJoOpnQ0l2SR9ODG7D5MyEZNyEbNOICakVv0okXzvbqmqVq/e/8Ch
6LoLqXkMHs1g2azjBgyPPLf9l5I75qZSEsmeIXHJtcW5JxDjMvRcYroafwgr2mYG8pnj/aFhhf9M
y+pU5J5ATMxhMfsk4uH4hhPm6Kn5uqdPn36wo6MrmeNYy9el6Orrb+zaHxPjYsl8+J8aA18zcqX8
yhKFQnH9tjZmCkV5ScmVm+9zWb8p8qGY+JquowltnYek1T+a7HCB5aoODi14XZrQ2pOSgZiUyeCR
pOa+naGFS+8ts3YrdjV//iK7iCOXD6bk9GFCKrJpWYhRRyuPSSR7R1gyQbI+Ylz44ct7oqXNl5Mz
OuRhkeezb2Kggjc4NTW1bFCr1KrbG4y35Zw9e3bynfaG//FhFlXjCtwyuKaubqtSpdJYTri1oyU7
N1c2BQBg4Tvv2JoTIQOIyjt0tHR7Rp4WEzKRS89DjImrvrR23d6JP6d27iYJ5s8hPxxbmZDaoUnO
QczIQ0zJbmndHZ65wBL9ZrYIv/xyvYsZRFVX17S4r09Rd1un9e7u6nNF5163JPyfpp295SrIPHZs
UmtrW4bZc0ZEVKvU+samG2Hp6enmBAkVITP2ivv2230jpcnXZLmFiCnZyOTK9Hgk9kqUOeoqGeD1
3quEmpm2aXvi9KS0hmt5xl51XFpuNxcecynQCKklkJlZJTQnY4qLS+b39MjPDmhp0F9ZVbV+8WJj
B8i79ZH7MzDhNrVUXFzyslyuqDD292OwvaPjfFJ6+gwT/I8HAPDd5uR/SJM62zLzEdNyEFOze7QR
h099YE5p/N4HNa/yN954w+loUqU0rwAxLQfZ7ALEQ7HVOa8uWeICAFBk6ilx+fLVj/r6jW9s0un1
2NzaGpecnOzzp1A3v1ItUeYk+dVrZSvPXzzv+5PqkfCir44md3LSdKMlcjSl9ur6TbGP32/xtrTn
9x88tSYprZtJSkdMyWbwaGJ9bfDerNkD1Bzv2rXqd4uKLv3jT6lufotastwg3/tUMiQ2qSYluwAx
KYth0/LVeOBwUfwnEuPeYIa63G/plJmk7vstqU8dTWiuzcpHTMlBTMvtNkQePfuZOXLmYzIMzIvp
Xlor/Nbxh3PUXOywPzw/TM9RFZ3tXSe9fSZH2ziOGm3gGOBxcra77fp/33njsQ0DmXe/WsSbqylv
a48v2eQ6YfLCPfaO3i+p1MDxBQzV2XLt6IVTSV9Mmjw3RKtWBE+YIMrv7OxEc1eXv9wwr5wVKza5
HolvVh5O6MToI52G5CzE5BzE2OSm67tCs+aaj7948fLrNXV1qy3CGr979VlKYXx8/KgbjY3fHton
HXlTDR4ulUhTOrnELMTkbMSDRxt6so4jhkZcDv+11tefbpg3wPDI8x9lFyDGZuiZo6lqLiGjn4mK
rktb/uZXHgAA+/dLB91obNx/M5/a1VV8+fLlZy1xN79W/1ruQ//+96fWtfX1X/X393UjIvb3KztK
y8oWmb/ftSf12cSErob4DNZwNF2PidnIxqY2y7/6LtTt5/BP92P8oW9TnT3bWC4ltB28mOWAoYAy
iB2tBJ0ddafeXuT1vJm4BUUFPD5vprVOq+WEIhE12MnpEUcHh8zG5mbphXPnvpkzZ07Vvaqlgeqm
tLzcb5iHR4C9vf1NS4amKQIAjgEBAVBWhoIJE0jWh5+FPDdj1uvn+DwHZFg9O8jB3W6420OvA8BW
UzMq5i+1+s2Wx8Zt6S9kFSBm5SNm5yOm5uvwSMJ1VfAP0umWqUQAgKysrJnt7e23hTX6lUrFterq
gKCgIHtLU9fylVVmu9wyOZSZeWxSS0tLCofcbeGDuus3Qnbs2O9ieZ5UKqX3hRduy5XpMTtPj5kF
DB77EfHg4Yr2ZcuW2d96L9pfaBM2dyPZtjPpebtBXk/0ytubeRSlZAl2Cmhlb2tnTf36bz5rBDTW
dlq+l6Wi4tq7np5ua21tbYeZr9fb21/d0tIq8fHxPvJz992584DzCy89vcbZadAHYrFYaJoLdHR1
5V65fHntvHnzLtxJmjZtkk63c/S05zilrQH49gwrcLLm6QfVNp/fuTnwi5Y7Nfb4Pzcs3/cVHBzs
1NjcuEWj0agtJaKzs/NEeXn5K9nZ2aPNKnThwndsCwpOTaqpqVnbq+htvj0e1VdZXHzl1T+rPf+H
T0QikVCzZwdQZniGEUkXB/5+fhzcZUVZrs6TZ89O9Box4jsX58HPUdQtg6S/v1/DcnidEGIgAA58
Ps/TysrqpsWkUqn6W1tbt23cuHFreHh4/y+9YVsikVDjxxthhuUWrWgC58xh/7DY/p/cibstrFFa
Wuqn6O29iL8wNFqNtrW9PSYrK8v7rxA+IH8BRpjb0SAA0MUlJQucnZ3n2IjFMwFxiKnVmY5Ftlqt
1uZdq6nJnTd79hULwnP/53X3/0ZYAwDA29vbdu7cufYAt95Z+T8RPvj/eZj7R/MG9rAzm6F/yjDx
X1kF3YPTZa5a/1vN/D3+Hn+Pv8ff4y81/h+ApVJ3tdMQ0wAAAABJRU5ErkJggg==
`

// authBrandCSS styles the shared logo lockup.
const authBrandCSS = `
  .brand{display:flex;align-items:center;gap:9px;justify-content:center}
  .mark{width:26px;height:26px;flex:none}
  .logo{font-size:20px;font-weight:700;letter-spacing:-0.02em}
`
