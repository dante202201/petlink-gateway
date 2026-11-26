<?php

namespace app\api\controller;

use app\common\controller\Api;
use app\common\model\UserTranslate;
use think\cache\driver\Redis;
use think\Config;
use think\Log;
use think\Utils;

/**
 * 翻译 / 语音合成接口
 */
class Translate extends Api
{
    protected $noNeedLogin = [''];
    protected $noNeedRight = ['*'];

    /**
     * 获取百度 AccessToken（对外接口）
     */
    public function getAccessToken()
    {
        $access_token = $this->fetchAccessToken();
        if (!$access_token) {
            $this->error('获取AccessToken失败');
        }
        $this->success('获取AccessToken成功', $access_token);
    }

    /**
     * 短文本在线合成
     * @see https://cloud.baidu.com/doc/SPEECH/s/mlbxh7xie
     *
     * 参数：
     * - text  必填，合成文本（建议不超过约60字；最长1024GBK字节）
     * - per / audio  选填，发音人，默认0（度小美）
     * - spd   选填，语速0-15，默认5
     * - pit   选填，音调0-15，默认5
     * - vol   选填，音量0-15，默认5
     * - aue   选填，3=mp3(默认) 4=pcm-16k 5=pcm-8k 6=wav
     */
    public function text2audio($text, $per = 0, $spd = 5, $pit = 5, $vol = 5, $aue = 3)
    {
        $gbkLen = strlen(@iconv('UTF-8', 'GBK//IGNORE', $text));
        if ($gbkLen > 1024) {
            Log::addlogsNew('文本过长，最长1024GBK字节', 'text2audio');
            $this->error('网络异常，请稍后再试[1001]');
        }

        $spd = max(0, min(15, $spd));
        $pit = max(0, min(15, $pit));
        $vol = max(0, min(15, $vol));
        if (!in_array($aue, [3, 4, 5, 6], true)) {
            $aue = 3;
        }

        $token = $this->fetchAccessToken();
        if (!$token) {
            Log::addlogsNew('网络异常，获取AccessToken失败', 'text2audio');
            $this->error('网络异常，请稍后再试[1002]');
        }

        $cuid = 'backend-pet';
        if ($this->auth && $this->auth->id) {
            $cuid = 'uid_' . $this->auth->id;
        } elseif ($this->request->ip()) {
            $cuid = 'ip_' . md5($this->request->ip());
        }

        // tex 先 urlencode 一次，http_build_query 再 encode 一次（百度推荐二次编码）
        $params = [
            'tex'  => urlencode($text),
            'tok'  => $token,
            'cuid' => $cuid,
            'ctp'  => 1,
            'lan'  => 'zh',
            'spd'  => $spd,
            'pit'  => $pit,
            'vol'  => $vol,
            'per'  => $per,
            'aue'  => $aue,
        ];

        $result = $this->requestBaiduTts($params);
        if ($result === false) {
            Log::addlogsNew('网络异常，语音合成请求失败', 'text2audio');
            $this->error('网络异常，请稍后再试[1003]');
        }

        // 合成失败时返回 JSON
        if (isset($result['is_audio']) && !$result['is_audio']) {
            $err = json_decode($result['body'], true);
            Log::addlogsNew($err ?: $result['body'], 'text2audio');
            $msg = isset($err['err_msg']) ? $err['err_msg'] : '语音合成失败';
            Log::addlogsNew($msg, 'text2audio');
            $this->error('网络异常，请稍后再试[1004]', $err ?: []);
        }

        $formatMap = [
            3 => 'mp3',
            4 => 'pcm',
            5 => 'pcm',
            6 => 'wav',
        ];
        $format = $formatMap[$aue];

        // 先落到本地临时文件，再按 Common 同款方式上传 OSS
        $uploadDir = Config::get('tempdir');
        if (!is_dir($uploadDir)) {
            @mkdir($uploadDir, 0755, true);
        }
        $fileName = md5($result['body'] . microtime(true)) . '.' . $format;
        $localPath = rtrim($uploadDir, DIRECTORY_SEPARATOR) . DIRECTORY_SEPARATOR . $fileName;
        if (file_put_contents($localPath, $result['body']) === false) {
            Log::addlogsNew('网络异常，音频临时文件写入失败', 'text2audio');
            $this->error('网络异常，请稍后再试[1005]');
        }

        $uid = ($this->auth && $this->auth->id) ? (int)$this->auth->id : 0;
        $object = 'app/audio/' . date('Ym') . '/' . $uid . '/' . $fileName;
        $data = $this->ossUpload($object, $localPath, 0);
        @unlink($localPath);

        if (!$data) {
            Log::addlogsNew('网络异常，音频上传失败', 'text2audio');
            $this->error('网络异常，请稍后再试[1006]');
        }

        $domain = Config::get('oss_img_domain') ?: Config::get('ali.oss_img_domain');
        return rtrim($domain, '/') . '/' . ltrim($object, '/');
    }

    /**
     * 获取并缓存 AccessToken
     * @return string|false
     */
    private function fetchAccessToken()
    {
        $redis = new Redis(['select' => 2]);
        $key = 'baidu_translate_access_token';
        $access_token = $redis->get($key);
        if ($access_token) {
            return $access_token;
        }

        $params = [
            'client_id'     => Config::get('third.baidu_translate_client_id'),
            'client_secret' => Config::get('third.baidu_translate_client_secret'),
            'grant_type'    => 'client_credentials',
        ];
        Log::addlogsNew($params, 'getAccessToken');
        $result = Utils::post_formdata('https://aip.baidubce.com/oauth/2.0/token', $params);
        Log::addlogsNew($result, 'getAccessToken');
        $result = json_decode($result, true);
        if (empty($result['access_token'])) {
            Log::addlogsNew($result, 'getAccessToken_fail');
            return false;
        }

        $access_token = $result['access_token'];
        // access_token 有效期约30天，缓存15天
        $redis->set($key, $access_token, 86400 * 15);
        return $access_token;
    }

    /**
     * 请求百度短文本合成接口
     * @param array $params
     * @return array|false ['is_audio'=>bool, 'content_type'=>string, 'body'=>string]
     */
    private function requestBaiduTts(array $params)
    {
        $url = 'https://tsn.baidu.com/text2audio';
        $body = http_build_query($params);

        $ch = curl_init();
        curl_setopt($ch, CURLOPT_URL, $url);
        curl_setopt($ch, CURLOPT_POST, 1);
        curl_setopt($ch, CURLOPT_POSTFIELDS, $body);
        curl_setopt($ch, CURLOPT_RETURNTRANSFER, 1);
        curl_setopt($ch, CURLOPT_HEADER, 1);
        curl_setopt($ch, CURLOPT_SSL_VERIFYPEER, false);
        curl_setopt($ch, CURLOPT_SSL_VERIFYHOST, false);
        curl_setopt($ch, CURLOPT_TIMEOUT, 30);
        curl_setopt($ch, CURLOPT_HTTPHEADER, [
            'Content-Type: application/x-www-form-urlencoded',
        ]);

        $response = curl_exec($ch);
        if ($response === false) {
            Log::addlogsNew(curl_error($ch), 'text2audio_curl');
            curl_close($ch);
            return false;
        }

        $headerSize = curl_getinfo($ch, CURLINFO_HEADER_SIZE);
        curl_close($ch);

        $header = substr($response, 0, $headerSize);
        $body = substr($response, $headerSize);

        $contentType = '';
        if (preg_match('/Content-Type:\s*([^\r\n;]+)/i', $header, $m)) {
            $contentType = trim($m[1]);
        }

        $isAudio = stripos($contentType, 'audio') === 0;

        return [
            'is_audio'     => $isAudio,
            'content_type' => $contentType,
            'body'         => $body,
        ];
    }


    /**
     * 宠物语音翻译：上传音频文件 或 传音频 URL，二选一
     * 参考 Common::upload 的 file 处理方式
     *
     * 参数：
     * - pet_type  必填
     * - audio     文件流（与 audio_url 二选一）
     * - audio_url 音频地址（与 audio 二选一）
     */
    public function translateAll()
    {
        $pet_type = trim((string)$this->request->param('pet_type', ''));
        $audio_duration = trim((string)$this->request->param('audio_duration', 0));

        $file = $this->request->file('audio');
        $audio_url = trim((string)$this->request->param('audio_url', ''));

        if ($file && $audio_url !== '') {
            Log::addlogsNew('内容与url只能传一个', 'translate');
            $this->error('网络异常，请稍后再试[2001]');
        }
        if (!$file && $audio_url === '') {
            Log::addlogsNew('内容与url不能都为空', 'translate');
            $this->error('网络异常，请稍后再试[2002]');
        }

        $per = $this->request->param('per', 0);
        $spd = $this->request->param('spd', 5);
        $pit = $this->request->param('pit', 5);
        $vol = $this->request->param('vol', 5);
        $aue = $this->request->param('aue', 3);

        $createtime = time();

        $ai_url = 'http://8.155.162.131';
        $ai_translate_port = Config::get('ai.ai_translate_port');
        $url = $ai_url . ':' . $ai_translate_port . '/translate';

        $params = ['pet_type' => $pet_type];
        $localPath = '';
        $originAudioUrl = '';

        if ($file) {
            // 与 Common::upload 一致：校验并落到 tempdir
            $upload = Config::get('upload');
            preg_match('/(\d+)(\w+)/', $upload['maxsize'], $matches);
            $type = strtolower($matches[2]);
            $typeDict = ['b' => 0, 'k' => 1, 'kb' => 1, 'm' => 2, 'mb' => 2, 'gb' => 3, 'g' => 3];
            $size = (int)$upload['maxsize'] * pow(1024, isset($typeDict[$type]) ? $typeDict[$type] : 0);

            $fileInfo = $file->getInfo();
            Log::addlogsNew($fileInfo, 'translate');
            if (empty($fileInfo) || empty($fileInfo['tmp_name'])) {
                Log::addlogsNew('No file upload or server upload limit exceeded', 'translate');
                $this->error('网络异常，请稍后再试2003');
            }
            if ($fileInfo['size'] > $size) {
                Log::addlogsNew('File size exceeds the limit', 'translate');
                $this->error('网络异常，请稍后再试[2004]');
            }

            $suffix = strtolower(pathinfo($fileInfo['name'], PATHINFO_EXTENSION));
            $suffix = $suffix && preg_match("/^[a-zA-Z0-9]+$/", $suffix) ? $suffix : 'mp3';
            $allowSuffix = ['mp3', 'wav', 'm4a', 'aac', 'amr', 'pcm', 'ogg', 'flac', 'webm'];
            if (!in_array($suffix, $allowSuffix, true)) {
                Log::addlogsNew('不支持的音频格式: ' . $suffix, 'translate');
                $this->error('网络异常，请稍后再试[2005]');
            }

            $uploadDir = Config::get('tempdir');
            if (!is_dir($uploadDir)) {
                @mkdir($uploadDir, 0755, true);
            }
            $fileName = date('YmdHis') . '_' . mt_rand(1000, 9999) . '.' . $suffix;
            $splInfo = $file->validate(['size' => $size])->move($uploadDir, $fileName);
            if (!$splInfo) {
                Log::addlogsNew('音频保存失败', 'translate');
                $this->error('网络异常，请稍后再试[2006]');
            }
            $localPath = $splInfo->getPathname();

            // 原音频上传 OSS
            $uid = ($this->auth && $this->auth->id) ? (int)$this->auth->id : 0;
            $object = 'app/audio/origin/' . date('Ym') . '/' . $uid . '/' . $fileName;
            $ossData = $this->ossUpload($object, $localPath, 0);
            if (!$ossData) {
                @unlink($localPath);
                Log::addlogsNew('原音频上传OSS失败', 'translate');
                $this->error('网络异常，请稍后再试[2007]');
            }
            $domain = Config::get('oss_img_domain') ?: Config::get('ali.oss_img_domain');
            $originAudioUrl = rtrim($domain, '/') . '/' . ltrim($object, '/');

            // 以真实文件字段传给 AI（CURLFile）
            $mime = !empty($fileInfo['type']) ? $fileInfo['type'] : 'application/octet-stream';
            $params['audio'] = new \CURLFile($localPath, $mime, $fileInfo['name']);
        } else {
            $originAudioUrl = $audio_url;
            $params['audio_url'] = $audio_url;
        }

        Log::addlogsNew([
            'url'              => $url,
            'pet_type'         => $pet_type,
            'mode'             => $file ? 'file' : 'url',
            'origin_audio_url' => $originAudioUrl,
            'audio_duration' => $audio_duration,
        ], 'translate');

        $resRaw = Utils::postMultipart($url, $params);
        if ($localPath && is_file($localPath)) {
            @unlink($localPath);
        }

        Log::addlogsNew($resRaw, 'translate');
        $res = json_decode($resRaw, true);
        if (!$res) {
            Log::addlogsNew('网络异常，翻译服务无响应', 'translate');
            $this->error('网络异常，请稍后再试[2008]');
        }

        // if (empty($res['is_supported'])) {
        //     $this->error(isset($res['message']) ? $res['message'] : '翻译失败', $res);
        // }

        $text = isset($res['pet_says']) ? $res['pet_says'] : '';
        // if ($text === '') {
        //     $this->error('翻译结果为空', $res);
        // }

        if (!empty($text)) {
            Log::addlogsNew('开始处理音频合成: ' . $text, 'translate');
            $resultAudioUrl = $this->text2audio($text, $per, $spd, $pit, $vol, $aue);
            if (empty($resultAudioUrl)) {
                Log::addlogsNew('音频合成失败', 'translate');
                $this->error('网络异常，请稍后再试[2009]');
            }
        } else {
            $resultAudioUrl = '';
        }


        $resulttime = time();

        $translate_id = UserTranslate::createId([
            'user_id' => $this->user_id,
            'origin_audio_url' => $originAudioUrl,
            'audio_url' => $resultAudioUrl,
            'translate_text' => $text,
            'origin_audio_duration' => $audio_duration,
            'pet_type' => $pet_type,
            'ai_result' => json_encode($res, JSON_UNESCAPED_UNICODE),
            'createtime' => $createtime,
            'resulttime' => $resulttime,
        ]);


        $this->success('翻译成功', [
            'id'               => $translate_id,
            'pet_says'         => $text,
            'origin_audio_url' => $originAudioUrl,
            'audio_url'        => $resultAudioUrl,
            'raw'              => $res,
            'result_says'      => empty($text) ? '当前音频未捕获有效猫狗声纹信号' : $text,
        ]);
    }



    public function getTranslateHistory()
    {
        $user_id = $this->user_id;
        $page = $this->request->param('page', 1);
        $page_size = $this->request->param('page_size', 10);
        $list = UserTranslate::where('user_id', $user_id)->order('createtime', 'desc')->paginate($page_size, false, ['page' => $page]);
        foreach ($list as $item) {
            $item['ai_result'] = json_decode($item['ai_result'], true);
            $item['result_says'] = empty($item['translate_text']) ? '当前音频未捕获有效猫狗声纹信号' : $item['translate_text'];
        }
        $this->success('获取翻译历史成功', $list);
    }

    public function translateFeedback()
    {
        Log::addlogsNew($this->request->param(), 'translateFeedback');
        $translate_id = $this->request->param('translate_id', 0);
        $feedback_type = $this->request->param('feedback_type', 0);
        $feedback_emotion_type = $this->request->param('feedback_emotion_type', 0);

        $translate = UserTranslate::where('id', $translate_id)->find();
        if (!$translate) {
            $this->error('翻译不存在');
        }

        $translate->feedback_type = $feedback_type;
        $translate->feedback_emotion_type = $feedback_emotion_type;
        $translate->save();
        $this->success('反馈成功', $translate);
    }
}
