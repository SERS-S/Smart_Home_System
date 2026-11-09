#include <time.h>
#include <stdio.h>
#include <stdint.h>

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#include "driver/i2c_master.h"
#include "driver/gpio.h"

#include "esp_log.h"
#include "nvs_flash.h"
#include "cJSON.h"

#define I2C_SDA_PIN GPIO_NUM_21
#define I2C_SCL_PIN GPIO_NUM_22
#define I2C_PORT    I2C_NUM_0
#define BUTTON_GPIO GPIO_NUM_33

#define BMP280_ADDR_1 0x76
#define BMP280_ADDR_2 0x77

#define BMP280_REG_ID       0xD0
#define BMP280_REG_RESET    0xE0
#define BMP280_REG_CALIB    0x88
#define BMP280_REG_CTRL     0xF4
#define BMP280_REG_CONFIG   0xF5
#define BMP280_REG_DATA     0xF7

#define BMP280_CHIP_ID 0x58

static const char *TAG = "bmp280";

static i2c_master_bus_handle_t bus_handle;
static i2c_master_dev_handle_t bmp280_handle;

#include "wifi_manager.h"
#include "mqtt_manager.h"

#define DEVICE_ID "temperature_sensor_01"
#define VERSION   "0.1"

#define MQTT_TOPIC_DATA "telemetry/home/temperature/data"
#define MQTT_TOPIC_INFO "telemetry/home/temperature/info"

#define PUBLISH_PERIOD_MS 60000

static int serial_number = 0;

typedef struct {
    uint16_t dig_T1;
    int16_t dig_T2;
    int16_t dig_T3;
} bmp280_calibration_t;

static bmp280_calibration_t calibration;

// Read BMP280 registers
static esp_err_t bmp280_read_regs(
    uint8_t reg,
    uint8_t *data,
    size_t len
)
{
    return i2c_master_transmit_receive(
        bmp280_handle,
        &reg,
        1,
        data,
        len,
        1000
    );
}

// Write BMP280 register
static esp_err_t bmp280_write_reg(uint8_t reg, uint8_t value)
{
    uint8_t data[2] = {reg, value};

    return i2c_master_transmit(
        bmp280_handle,
        data,
        sizeof(data),
        1000
    );
}

// Read little-endian unsigned 16-bit value
static uint16_t read_u16_le(const uint8_t *data)
{
    return (uint16_t)data[0] | ((uint16_t)data[1] << 8);
}

// Read little-endian signed 16-bit value
static int16_t read_i16_le(const uint8_t *data)
{
    return (int16_t)read_u16_le(data);
}

// Initialize I2C bus
static esp_err_t i2c_init(void)
{
    i2c_master_bus_config_t bus_config = {
        .i2c_port = I2C_PORT,
        .sda_io_num = I2C_SDA_PIN,
        .scl_io_num = I2C_SCL_PIN,
        .clk_source = I2C_CLK_SRC_DEFAULT,
        .glitch_ignore_cnt = 7,
        .flags.enable_internal_pullup = true,
    };

    return i2c_new_master_bus(&bus_config, &bus_handle);
}

// Find BMP280 at address 0x76 or 0x77
static esp_err_t bmp280_find(void)
{
    uint8_t chip_id;

    const uint8_t addresses[] = {
        BMP280_ADDR_1,
        BMP280_ADDR_2
    };

    for (int i = 0; i < 2; i++) {
        i2c_device_config_t dev_config = {
            .dev_addr_length = I2C_ADDR_BIT_LEN_7,
            .device_address = addresses[i],
            .scl_speed_hz = 100000,
        };

        esp_err_t err = i2c_master_bus_add_device(
            bus_handle,
            &dev_config,
            &bmp280_handle
        );

        if (err != ESP_OK) {
            continue;
        }

        err = bmp280_read_regs(BMP280_REG_ID, &chip_id, 1);


        if (err == ESP_OK && chip_id == BMP280_CHIP_ID) {
            ESP_LOGI(TAG, "BMP280 found at address 0x%02X",
                     addresses[i]);
            return ESP_OK;
        }

        i2c_master_bus_rm_device(bmp280_handle);
        bmp280_handle = NULL;
    }

    ESP_LOGE(TAG, "BMP280 not found!");
    return ESP_ERR_NOT_FOUND;
}

// Read factory calibration coefficients
static esp_err_t bmp280_read_calibration(void)
{
    uint8_t data[24];

    esp_err_t err = bmp280_read_regs(
        BMP280_REG_CALIB,
        data,
        sizeof(data)
    );

    if (err != ESP_OK) {
        return err;
    }

    calibration.dig_T1 = read_u16_le(&data[0]);
    calibration.dig_T2 = read_i16_le(&data[2]);
    calibration.dig_T3 = read_i16_le(&data[4]);

    ESP_LOGI(TAG, "Calibration coefficients:");
    ESP_LOGI(TAG, "T1: %u", calibration.dig_T1);
    ESP_LOGI(TAG, "T2: %d", calibration.dig_T2);
    ESP_LOGI(TAG, "T3: %d", calibration.dig_T3);

    return ESP_OK;
}

// Initialize BMP280
static esp_err_t bmp280_init(void)
{
    ESP_ERROR_CHECK(i2c_init());

    esp_err_t err = bmp280_find();

    if (err != ESP_OK) {
        return err;
    }

    // Soft reset
    err = bmp280_write_reg(BMP280_REG_RESET, 0xB6);

    if (err != ESP_OK) {
        return err;
    }

    vTaskDelay(pdMS_TO_TICKS(100));

    err = bmp280_read_calibration();

    if (err != ESP_OK) {
        return err;
    }

    // Temperature oversampling x1
    // Pressure oversampling x1
    // Normal mode
    // Configure CONFIG while sensor is in Sleep mode
    err = bmp280_write_reg(BMP280_REG_CONFIG, 0x00);

    if (err != ESP_OK) {
        return err;
    }

    uint8_t config_read;
    err = bmp280_read_regs(BMP280_REG_CONFIG, &config_read, 1);

    if (err != ESP_OK) {
        return err;
    }

    ESP_LOGI(TAG, "CONFIG: wrote 0x00, read 0x%02X", config_read);

    // Enable temperature and pressure oversampling x1, Normal mode
    err = bmp280_write_reg(BMP280_REG_CTRL, 0x27);

    if (err != ESP_OK) {
        return err;
    }

    uint8_t ctrl_read;
    err = bmp280_read_regs(BMP280_REG_CTRL, &ctrl_read, 1);

    if (err != ESP_OK) {
        return err;
    }

    ESP_LOGI(TAG, "CTRL_MEAS: wrote 0x27, read 0x%02X", ctrl_read);

    vTaskDelay(pdMS_TO_TICKS(100));

    // Read registers back
    uint8_t regs[3];

    err = bmp280_read_regs(0xF3, regs, sizeof(regs));

    if (err != ESP_OK) {
        return err;
    }

    ESP_LOGI(TAG,
            "After init: STATUS=0x%02X CTRL_MEAS=0x%02X CONFIG=0x%02X",
            regs[0], regs[1], regs[2]);



    return ESP_OK;
}




// Read compensated temperature in Celsius
static esp_err_t bmp280_read_temperature(float *temperature)
{
    uint8_t data[6];


    esp_err_t err = bmp280_read_regs(
        BMP280_REG_DATA,
        data,
        sizeof(data)
    );

    if (err != ESP_OK) {
        return err;
    }

    // Temperature ADC value (20 bits)
    int32_t adc_T =
        ((int32_t)data[3] << 12) |
        ((int32_t)data[4] << 4) |
        ((int32_t)data[5] >> 4);

    if (adc_T == 0x80000) {
        ESP_LOGW(TAG, "BMP280 returned skipped temperature measurement");
        return ESP_ERR_INVALID_RESPONSE;
    }

    uint32_t raw_temp =
    ((uint32_t)data[3] << 12) |
    ((uint32_t)data[4] << 4) |
    ((uint32_t)data[5] >> 4);

    ESP_LOGI(TAG, "Raw temperature ADC: %lu",
            (unsigned long)raw_temp);

    uint8_t regs[3];

    esp_err_t ret = bmp280_read_regs(0xF3, regs, sizeof(regs));






    if (ret == ESP_OK) {
        ESP_LOGI(TAG,
                "STATUS=0x%02X CTRL_MEAS=0x%02X CONFIG=0x%02X",
                regs[0], regs[1], regs[2]);
    } else {
        ESP_LOGE(TAG, "Failed to read BMP280 registers: %s",
                esp_err_to_name(ret));
    }

    int32_t var1;
    int32_t var2;
    int32_t t_fine;

    var1 = ((((adc_T >> 3) -
             ((int32_t)calibration.dig_T1 << 1))) *
             (int32_t)calibration.dig_T2) >> 11;

    var2 = (((((adc_T >> 4) -
              (int32_t)calibration.dig_T1) *
              ((adc_T >> 4) -
              (int32_t)calibration.dig_T1)) >> 12) *
              (int32_t)calibration.dig_T3) >> 14;

    t_fine = var1 + var2;

    int32_t temp_x100 = (t_fine * 5 + 128) >> 8;

    *temperature = temp_x100 / 100.0f;

    return ESP_OK;
}

static void button_init(void)
{
    gpio_config_t config = {
        .pin_bit_mask = 1ULL << BUTTON_GPIO,
        .mode = GPIO_MODE_INPUT,
        .pull_up_en = GPIO_PULLUP_ENABLE,
        .pull_down_en = GPIO_PULLDOWN_DISABLE,
        .intr_type = GPIO_INTR_DISABLE,
    };

    ESP_ERROR_CHECK(gpio_config(&config));
}

static bool button_is_pressed(void)
{
    return gpio_get_level(BUTTON_GPIO) == 0;
}

static void publish_telemetry(float temperature)
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
        "temperature",
        temperature
    );

    cJSON_AddStringToObject(
        root,
        "unit",
        "celsius"
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
        ESP_LOGE(TAG, "Failed to serialize JSON");

        cJSON_Delete(root);
        return;
    }

    int msg_id = mqtt_manager_publish(
        MQTT_TOPIC_DATA,
        payload,
        1,
        0
    );

    if (msg_id >= 0) {

        ESP_LOGI(
            TAG,
            "Published to %s: %s",
            MQTT_TOPIC_DATA,
            payload
        );

    } else {

        ESP_LOGE(
            TAG,
            "MQTT publish failed"
        );
    }

    cJSON_free(payload);
    cJSON_Delete(root);
}


static void telemetry_task(void *arg)
{
    (void)arg;

    while (1) {

        float temperature;

        esp_err_t err =
            bmp280_read_temperature(&temperature);

        if (err == ESP_OK) {

            ESP_LOGI(
                TAG,
                "Temperature: %.2f C",
                temperature
            );

            publish_telemetry(temperature);

        } else {

            ESP_LOGE(
                TAG,
                "Temperature read failed: %s",
                esp_err_to_name(err)
            );
        }

        vTaskDelay(
            pdMS_TO_TICKS(PUBLISH_PERIOD_MS)
        );
    }
}

static void button_task(void *arg)
{
    (void)arg;

    while (1) {

        if (button_is_pressed()) {
            ESP_LOGI("BUTTON", "Button pressed");
        }

        vTaskDelay(
            pdMS_TO_TICKS(50)
        );
    }
}

void app_main(void)
{
    esp_err_t ret = nvs_flash_init();

    if (ret == ESP_ERR_NVS_NO_FREE_PAGES ||
        ret == ESP_ERR_NVS_NEW_VERSION_FOUND) {

        ESP_ERROR_CHECK(nvs_flash_erase());

        ret = nvs_flash_init();
    }

    ESP_ERROR_CHECK(ret);

    ESP_LOGI(
        TAG,
        "Starting BMP280 temperature sensor"
    );

    ESP_ERROR_CHECK(bmp280_init());

    button_init();

    // Common network modules.
    ESP_ERROR_CHECK(wifi_manager_init());

    wifi_manager_wait_connected();

    ESP_ERROR_CHECK(mqtt_manager_init());

    mqtt_manager_wait_connected();

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
}