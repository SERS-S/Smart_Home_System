#include <stdio.h>
#include <time.h>

#include "esp_log.h"
#include "esp_err.h"
#include "esp_sntp.h"
#include "nvs_flash.h"

#include "esp_adc/adc_oneshot.h"

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "freertos/event_groups.h"

#include "driver/gpio.h"

#include "cJSON.h"

#include "wifi_manager.h"
#include "mqtt_manager.h"
#include "button.h"
#include "control.h"
#include "time_sync.h"
#include "publish_connection.h"
#include "info.h"

// ---------- Configuration ----------

#define MQTT_TOPIC_DATA      "telemetry/home/light/data"
#define MQTT_TOPIC_INFO      "telemetry/home/light/info"
#define MQTT_TOPIC_SIGNAL      "telemetry/home/light/signal"

#define DEVICE_ID            "light_sensor_01"
#define VERSION              "0.1"

#define SENSOR_ADC_UNIT      ADC_UNIT_1
#define SENSOR_ADC_CHANNEL   ADC_CHANNEL_6   // GPIO34

#define PUBLISH_PERIOD_MS 120000
#define N_PROBS 30
#define SENSOR_PERIOD_MS (PUBLISH_PERIOD_MS) / (N_PROBS)

#define ESP_DISCONNECT 0x2001

#define LED_BUILTIN 2 

#define LUX_PER_ADC_COUNT    1.0f

#define MAX_ADC_VALUE        4095
#define VOLT_PER_ADC_COUNT   (3.0f / MAX_ADC_VALUE)


// ---------- State ----------

static const char *TAG = "light_sensor";

extern adc_oneshot_unit_handle_t adc_handle;

static int serial_number = 0;


// ---------- Sensor ----------

static void sensor_init(void)
{
    const adc_oneshot_unit_init_cfg_t unit_config = {
        .unit_id = SENSOR_ADC_UNIT,
    };

    const adc_oneshot_chan_cfg_t channel_config = {
        .atten = ADC_ATTEN_DB_12,
        .bitwidth = ADC_BITWIDTH_DEFAULT,
    };

    ESP_ERROR_CHECK(
        adc_oneshot_config_channel(
            adc_handle,
            SENSOR_ADC_CHANNEL,
            &channel_config
        )
    );

    ESP_LOGI(TAG, "TEMT6000 initialized");
}


static int sensor_read_raw(void)
{
    int value = 0;

    esp_err_t err = adc_oneshot_read(
        adc_handle,
        SENSOR_ADC_CHANNEL,
        &value
    );

    if (err != ESP_OK) {
        ESP_LOGE(
            TAG,
            "ADC read failed: %s",
            esp_err_to_name(err)
        );

        return -1;
    }

    return value;
}


static float raw_to_lux(int raw)
{
    return (float)raw * LUX_PER_ADC_COUNT;
}

static float read_lux()
{
    int raw = sensor_read_raw();

    return raw_to_lux(raw);
}
// ---------- Info ----------

static void info_task(void *args)
{
    (void)args;

    while (1) {
        wait_bits();

        publish_info(
            DEVICE_ID,
            VERSION,
            MQTT_TOPIC_INFO
        );

        vTaskDelay(
            pdMS_TO_TICKS(PUBLISH_PERIOD_MS)
        );
    }
}


// ---------- Telemetry ----------

static void publish_telemetry(int lux)
{
    time_t now;
    struct tm timeinfo;

    time(&now);

    gmtime_r(&now, &timeinfo);

    char timestamp[32];

    strftime(
        timestamp,
        sizeof(timestamp),
        "%Y-%m-%dT%H:%M:%SZ",
        &timeinfo
    );

    cJSON *root = cJSON_CreateObject();

    if (root == NULL) {
        ESP_LOGE(TAG, "Failed to create JSON");
        return;
    }

    cJSON_AddStringToObject(
        root,
        "device_id",
        DEVICE_ID
    );

    cJSON_AddNumberToObject(
        root,
        "illuminance",
        lux
    );

    cJSON_AddStringToObject(
        root,
        "unit",
        "lux"
    );

    cJSON_AddNumberToObject(
        root,
        "serial",
        ++serial_number
    );

    cJSON_AddStringToObject(
        root,
        "timestamp",
        timestamp
    );

    char *payload = cJSON_PrintUnformatted(root);

    if (payload == NULL) {
        cJSON_Delete(root);
        return;
    }

    int msg_id = mqtt_manager_publish(
        MQTT_TOPIC_DATA,
        payload,
        1,
        0
    );

    if (msg_id < 0) {
        ESP_LOGE(TAG, "Failed to publish telemetry");
    }

    cJSON_free(payload);
    cJSON_Delete(root);
}

static float mean(float *array, int n)
{
    int i;
    float sum = 0;
    for (i = 0; i < n; i++) {
        sum += array[i];
    }

    return sum / n;
}

static esp_err_t n_times_read(float *result)
{
    float array[N_PROBS];
    float lux;
    int i;

    for (i = 0; i < N_PROBS; i++) {
        lux = read_lux();

        array[i] = lux;

        ESP_LOGI(
            TAG,
            "Lux: %.2f lux",
            lux
        );

        vTaskDelay(
            pdMS_TO_TICKS(SENSOR_PERIOD_MS)
        );

        if (!bits_is_set()) {
            return ESP_DISCONNECT;
        }
    }

    *result = mean(array, N_PROBS);

    return ESP_OK;
}

static void telemetry_task(void *arg)
{
    (void)arg;

    while (1) {
        wait_bits();

        float lux;
        esp_err_t err = n_times_read(&lux);

        if (err == ESP_DISCONNECT) {
            continue;
        }

        if (err == ESP_OK) {

            ESP_LOGI(
                TAG,
                "Mean lux: %.2f lux",
                lux
            );

            publish_telemetry(lux);

        } else {

            ESP_LOGE(
                TAG,
                "Lux read failed: %s",
                esp_err_to_name(err)
            );
        }
    }
}


static void publish_connect(void)
{
    publish_connection(
        1,
        DEVICE_ID,
        MQTT_TOPIC_SIGNAL
    );
    gpio_set_level(LED_BUILTIN, 1);
}

static void publish_disconnect(void)
{
    publish_connection(
        0,
        DEVICE_ID,
        MQTT_TOPIC_SIGNAL
    );
    gpio_set_level(LED_BUILTIN, 0);
}

static void button_task(void *arg)
{
    (void)arg;

    while (1) {
        do_control(
            button_is_pressed,
            publish_disconnect,
            publish_connect
        );
    }
}


// ---------- Application ----------

void app_main(void)
{
    esp_err_t ret = nvs_flash_init();

    if (ret == ESP_ERR_NVS_NO_FREE_PAGES ||
        ret == ESP_ERR_NVS_NEW_VERSION_FOUND) {
        ESP_ERROR_CHECK(nvs_flash_erase());
        ret = nvs_flash_init();
    }

    ESP_ERROR_CHECK(ret);

    button_init();
    voltage_init();
    sensor_init();


    gpio_reset_pin(LED_BUILTIN);
    gpio_set_direction(LED_BUILTIN, GPIO_MODE_OUTPUT);

    // Network initialization.
    ESP_ERROR_CHECK(wifi_manager_init());

    wifi_manager_wait_connected();

    ESP_ERROR_CHECK(mqtt_manager_init());

    mqtt_manager_wait_connected();

    time_sync();

    init_event_group(publish_connect);

    if (xTaskCreate(
            telemetry_task,
            "telemetry",
            4096,
            NULL,
            5,
            NULL
        ) != pdPASS) {

        ESP_LOGE(
            TAG,
            "Failed to create telemetry task"
        );
    }

    if (xTaskCreate(
            button_task,
            "button",
            4096,
            NULL,
            5,
            NULL
        ) != pdPASS) {

        ESP_LOGE(
            TAG,
            "Failed to create button task"
        );
    }

    if (xTaskCreate(
            info_task,
            "info",
            4096,
            NULL,
            5,
            NULL
        ) != pdPASS) {

        ESP_LOGE(
            TAG,
            "Failed to create info task"
        );
    }
}