<template>
  <div id="desktop"></div>
</template>
<script>
import occamy from './occamy'
export default {
  data () { return {} },
  methods: {},
  mounted () {
    const url = 'ws://0.0.0.0:5636/api/v1/connect'
    let guac = new occamy.Client(new occamy.WebSocketTunnel(url));
    let display = guac.getDisplay()
    document.getElementById('desktop').appendChild(display.getElement());
    guac.onerror = null;
    guac.connect(`token=${this.$route.query.token}`)
    window.display = display;
    display.scale(Math.min(
      window.innerHeight / 1024,
      window.innerWidth / 768
    ))
    window.onunload = () => { guac.disconnect() }
    window.onresize = () => {
      display.scale(Math.min(
        window.innerHeight / display.getHeight(),
        window.innerWidth / display.getWidth()
      ))
    }
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
