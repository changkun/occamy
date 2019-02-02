<template>
  <div id="desktop"></div>
</template>
<script>
import Guacamole from 'guacamole-client'
export default {
  data () { return {} },
  methods: {},
  mounted (){
    const url = 'ws://0.0.0.0:5636/api/v1/connect'
    let guac = new Guacamole.Client(new Guacamole.WebSocketTunnel(url));
    document.getElementById('desktop').appendChild(guac.getDisplay().getElement());
    guac.onerror = (error) => { console.log(error) };
    guac.connect(`token=${this.$route.query.token}`)
    guac.getDisplay().scale(Math.min(window.innerWidth/1024, window.innerHeight/768))
    window.display = guac.getDisplay();
    window.guac = guac
    window.onunload = () => { guac.disconnect() }
    window.onresize = () => {
      guac.getDisplay().scale(Math.min(
        window.innerHeight / guac.getDisplay().getHeight(),
        window.innerWidth / guac.getDisplay().getWidth()
      ))
    }
    var mouse = new Guacamole.Mouse(guac.getDisplay().getElement());
    mouse.onmousedown = mouse.onmouseup = mouse.onmousemove = (mouseState) => {
      guac.getDisplay().showCursor(false);
      const scaledState = new Guacamole.Mouse.State(
          mouseState.x / guac.getDisplay().getScale(),
          mouseState.y / guac.getDisplay().getScale(),
          mouseState.left,
          mouseState.middle,
          mouseState.right,
          mouseState.up,
          mouseState.down
      );
      guac.sendMouseState(scaledState);
    };
    var keyboard = new Guacamole.Keyboard(document);
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
