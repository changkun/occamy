<template>
<div class="login">
    <el-form ref="form" :model="form" labelPosition="left" label-width="100px">
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
            win.location = this.$router.resolve({
                name: 'desktop', 
                query:{token: response.data.token}
            }).href
        }).catch((err) => {
            this.$message({message: 'login fail', type: 'error'})
        })
    },
  },
  computed: {
    showUsername() {
        if (this.form.protocol != 'vnc') {
            this.form.username = ''
        }
        return 
    }
  },
};
</script>

<style>
.login {
    padding: 50px;
}
</style>