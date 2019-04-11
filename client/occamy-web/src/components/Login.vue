<template>
<div>
<div class="login">
    <h1>Occamy Web Client</h1>
    <el-form ref="form" :model="form" labelPosition="left" label-width="200px">
        <el-form-item label="Protocol">
            <el-select v-model="form.protocol" class="protocols">
            <el-option label="VNC" value="vnc"></el-option>
            <el-option label="RDP" value="rdp"></el-option>
            <el-option label="SSH" value="ssh"></el-option>
            </el-select>
        </el-form-item>
        <el-form-item label="Host" prop="host">
            <el-input v-model="form.host"></el-input>
        </el-form-item>
        <el-form-item label="Username" v-show="showUsername">
            <el-input v-model="form.username"></el-input>
        </el-form-item>
        <el-form-item label="Password" prop="password">
            <el-input v-model="form.password"></el-input>
        </el-form-item>
        <el-form-item>
            <el-button type="primary" @click.native="login">Login</el-button>
        </el-form-item>
    </el-form>
</div>
</div>
</template>
<script>
import axios from 'axios'
export default {
  data() {
    return {
      records: [],
      exists: [],
      form: {
        protocol: 'vnc',
        host: '172.16.238.11:5901',
        username: '',
        password: 'vncpassword'
      },
    };
  },
  methods: {
    login() {
        axios.post('/api/v1/login', this.form).then((response) => {
            window.location = this.$router.resolve({
                name: 'desktop', 
                query:{token: response.data.token}
            }).href
        }).catch((err) => {
            this.$message({message: `login fail: ${err}`, type: 'error'})
        })
    },
  },
  computed: {
    showUsername() {
        return this.form.protocol != 'vnc'
    }
  }
};
</script>

<style>
.login {
    margin-left: 50px;
    text-align: left;
    padding: 50px;
    width: 500px;
}
</style>