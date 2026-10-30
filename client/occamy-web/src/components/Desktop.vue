<template>
  <div id="desktop"></div>
</template>
<script>
import occamy from './occamy'
export default {
  data () { return {} },
  methods: {},
  mounted () {
    const scheme = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const url = `${scheme}//${window.location.host}/api/v0/connect`
    let guac = new occamy.Client(new occamy.WebSocketTunnel(url));
    let display = guac.getDisplay()
    document.getElementById('desktop').appendChild(display.getElement());
    guac.onerror = null;
    // Display hints let guacd size the remote desktop to this window.
    const hints = new URLSearchParams({
      token: this.$route.query.token,
      width: window.innerWidth,
      height: window.innerHeight,
      dpi: Math.round(window.devicePixelRatio * 96),
      timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
    })
    guac.connect(hints.toString())
    window.display = display;
    // Fit the remote desktop to the window whenever either changes size.
    const fit = () => {
      if (display.getWidth() && display.getHeight()) {
        display.scale(Math.min(
          window.innerHeight / display.getHeight(),
          window.innerWidth / display.getWidth()
        ))
      }
    }
    display.onresize = fit
    window.onunload = () => { guac.disconnect() }
    window.onresize = fit
    var mouse = new occamy.Mouse(display.getElement());
    mouse.onmousedown = mouse.onmouseup = mouse.onmousemove = (mouseState) => {
      display.showCursor(false);
      const scaledState = new occamy.Mouse.State(
          mouseState.x / display.getScale(),
          mouseState.y / display.getScale(),
          mouseState.left,
          mouseState.middle,
          mouseState.right,
          mouseState.up,
          mouseState.down
      );
      guac.sendMouseState(scaledState);
    };
    var keyboard = new occamy.Keyboard(document);
    keyboard.onkeydown = (k) => { guac.sendKeyEvent(1, k) };
    keyboard.onkeyup = (k) => { guac.sendKeyEvent(0, k) };
  }
}
</script>

<style>
body {
  width: 100%;
  height: 100%;
  margin: 0;
  padding: 0;
}
#desktop {
  background: #000;
}
</style>
